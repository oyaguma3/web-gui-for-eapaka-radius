# web-gui-for-eapaka-radius

EAP-AKA RADIUS PoC（eapaka-radius-server-poc。以下「本PoC」）の管理 GUI（BFF + Web GUI。コマンド名 `eapaka-webgui`）。本PoCの Provisioning API（provisioning-api）だけを使い、加入者データや鍵情報は自分では保存しない。

このファイルは、本PoC側の検討・実装セッション（2026-10-07〜08。provisioning-api の実装と、この BFF の設計方針の合意）からの引き継ぎメモを兼ねる。

## 現在の状態

- 設計概要（`docs/design-overview.md`）を作成済み。実装はまだない（初版のステップ1から始める）。
- 初版は 5 つのステップで進める（`docs/design-overview.md` §9）。各ステップの終わりに「作ったもの」と「実際に動かして確かめたこと」を報告して確認をもらう。
  1. 骨格（module、コマンド、設定、HTTPS サーバー、compose）
  2. Provisioning API クライアントと契約テスト（`gen-client-cert`、`check-admin`、diagnose）。CI で provisioning-api を動かす方法もここで決める（推奨・合意済み: 本PoCを固定のコミットで checkout してビルドする）
  3. アカウントとセッション（aka 版からの移植）
  4. 画面（ダッシュボード、加入者、RADIUSクライアント、認可ポリシー、監査ログ）
  5. compose・運用ガイド・実機（simwifi）での通しの確認
- 初版の後は、本PoCの Provisioning API の拡張（D-13 §10。監査ログの参照等）→ eapaka-node-provisioner（統合API、別リポジトリ）の設計、の順に進める（ユーザー決定。`docs/design-overview.md` §10）。
- `docs/screen-spec.md` と `docs/operation-guide.md` はまだない。画面・手順を作ったら合わせて書く。

## 最初に読むもの

| ファイル | 内容 |
|---|---|
| `docs/design-overview.md` | このリポジトリの設計。決定事項、権限、画面、Provisioning API との連携、進め方 |
| `../eapaka-radius-server-poc/docs/openapi/provisioning-api.yaml` | Provisioning API の仕様。BFF とサーバーの間の契約 |
| `../eapaka-radius-server-poc/docs/D-13_Provisioning_API詳細設計書_r*.md` | Provisioning API の設計（mTLS、エラー、監査ログ、共有ネットワーク） |
| `../eapaka-radius-server-poc/docs/B-02_アプリケーションデプロイ手順書_r*.md` §15 | provisioning-api の有効化、BFF の登録、同じホストの BFF の共有ネットワーク（§15.11） |
| `../web-gui-for-aka-only-server/`（CLAUDE.md、docs/、internal/） | 手本にする実装（以下「aka 版」）。作法・コードはこれに揃える |

本PoCのドキュメントはファイル名に版数が付いている（例 `_r4.md`）。最新の版をファイル一覧で確かめて読む。

## 実装方針（aka 版と同じ流儀にそろえる）

- Go 1.27.1。Go のコードを書く前に `modern-go-guidelines:use-modern-go` スキルでガイドラインを確認する。
- 標準ライブラリを優先する。外部依存は次の 2 つに限る。
  - `github.com/valkey-io/valkey-go`: BFF 専用 Valkey への接続
  - `golang.org/x/crypto`: argon2id（パスワードハッシュ）
- HTTP は `net/http`（メソッドつきの `ServeMux` パターンと `r.PathValue`）。Gin は使わない（本PoCの provisioning-api は Gin だが、こちらは aka 版に揃える）。
- JSON は `encoding/json/v2`。
- テンプレートは `html/template`、静的ファイルは `embed`。HTMX と Pico CSS はファイルを同梱して配信する。Alpine.js は使わない。
- ログは `log/slog` の JSON を標準出力に出す。ローテーションは Docker に任せる。
- 設定は環境変数だけで受け取る（`godotenv` は使わない）。接頭辞は `EAPAKA_WEBGUI_`。compose の `.env` から渡す。
- module パスは `github.com/oyaguma3/web-gui-for-eapaka-radius`。
- 公開ポートの既定は `127.0.0.1:8445`（aka 版の 8444 と同じホストで並べられるように）。
- aka 版の認証・セッション・証明書・描画まわりのコードは、コピーして手直しする（共有モジュールにはしない）。
- コードのコメントとドキュメントは日本語で書く。

## 設計上の決定事項（詳細は `docs/design-overview.md`）

- 1 つの BFF が扱うのは本PoCの 1 ノード（provisioning-api 1 つ）。複数ノードや aka-only-server をあわせて扱うのは eapaka-node-provisioner の役目。Provisioning API の呼び出しはインターフェースの後ろに隠し、将来付け替えやすくする。
- アカウント・権限・セッションは BFF が持つ（BFF 専用の Valkey）。最初の管理者・管理者・一般ユーザーの 3 種類（aka 版と同じ）。
- 権限判定は BFF だけで行う。provisioning-api は BFF を全権の管理クライアントとして扱う。
- 一般ユーザーができないこと: 登録済み加入者の Ki / OPc の閲覧・変更、SQN / AMF の変更、RADIUSクライアントの登録・変更・削除、共有シークレットの閲覧、監査ログの閲覧、アカウント管理。加入者の登録・削除と認可ポリシーの作成・変更・削除はできる。
- Ki / OPc（`GET /subscribers/{imsi}/keys`）と共有シークレット（`GET /clients/{ip}/secret`）は、詳細画面のボタンを押したときだけ取得して表示する。取得は provisioning-api の監査ログに残る。
- provisioning-api を呼ぶときは、操作者のユーザーID を `X-Operator-Id` ヘッダーで渡す（形式 `^[A-Za-z0-9._@-]{1,64}$`）。変更操作と秘密の値の取得は、操作者がなければ送らない。
- 認可ポリシーは加入者とは独立に扱う（接続方式01の加入者は `policy:` だけを持つ）。編集画面ではルールの行を HTMX で増減・並べ替えし、PUT で全体を置き換える。
- ブラウザ向けの HTTPS は BFF 自身で終端する（自己署名の自動生成または持ち込み）。GUI はインターネットに直接公開せず、VPN 越しのアクセスを基本とする。CSRF 対策は `CrossOriginProtection`。
- 初版の範囲外（Provisioning API の拡張で検討）: provisioning-api の監査ログ・サーバーログの参照、セッション・統計、CSV の一括操作。

## Provisioning API との接続

- mTLS 必須。provisioning-api にはクライアント証明書を発行する機能がないので、BFF 側の `eapaka-webgui gen-client-cert` で証明書と秘密鍵を作り、SHA-256 フィンガープリントを本PoCの `.env` の `PROVISIONING_API_ADMIN_CLIENTS` に `識別名=フィンガープリント` で登録する（大文字・コロン区切りのままでよい。識別名は provisioning-api の監査ログの `mgmt_client` になる）。
- provisioning-api のサーバー証明書は本PoC側で openssl で作る自己署名。BFF はそれを検証用に固定し、ホスト名も検証する。
- 同一ホスト: 本PoC側が作る共有 Docker ネットワーク（既定名 `eapaka-prov`。本PoCを `docker compose --profile provisioning up -d` したときだけ作られる）に external として参加し、`https://provisioning-api:9444/admin/v1` に接続する。サーバー証明書の SAN に `DNS:provisioning-api` が要る。BFF の専用 Valkey は参加させない。
  - 本PoC側を先に起動する（ないと `network eapaka-prov declared as external, but could not be found`）。BFF が参加したまま本PoC側を止めても、ネットワークは「still in use」で残り、本PoCを起動し直せば BFF は再起動なしで接続できる。
- 別ホスト: 本PoC側で `PROVISIONING_API_BIND` を VPN のアドレスにし、サーバー証明書の SAN にそのアドレスを入れる。
- provisioning-api のログの `src_ip` は BFF のアドレスとは限らない（共有ネットワーク経由なら BFF コンテナの IP、ポート公開経由ならゲートウェイ IP になることがある）。BFF の識別は `mgmt_client` で行う。
- エラーは ProblemDetails（`cause` / `invalidParams`）。aka 版の対応表に `CLIENT_NOT_FOUND`・`POLICY_NOT_FOUND`・`CLIENT_ALREADY_EXISTS` を加える。PATCH は `application/merge-patch+json`（`application/json` も可）、未知の項目は 400 `INVALID_MSG_FORMAT`。
- 本PoCの Admin TUI と同時に使った場合の 404 / 409 は、他の操作で変更・削除されたと分かる文で出す。

## 検証の進め方

- 単体テストに加えて、BFF 専用 Valkey の結合テストと provisioning-api との契約テストは、接続先を環境変数で指定したときだけ実行する形にする（aka 版と同じ）。契約テストはテスト用の IMSI（`00101...`）・IP を作って最後に消す。
- 手元（WSL）で provisioning-api を相手にするときは、本PoCの `apps/provisioning-api` をビルドしたバイナリを、専用の Valkey コンテナ（別ポート）とテスト用の証明書で起動すればよい（本PoCのセッションで、`PROVISIONING_API_LISTEN_ADDR=127.0.0.1:19444`、`REDIS_HOST=127.0.0.1`、`REDIS_PORT=16379` 等で起動して確かめた）。手元の Docker では本PoCの開発用のスタック（`deployments-*`、Valkey は 127.0.0.1:6379）や別プロジェクトのコンテナが動いているので、それらには触れず、通しの確認にも使わない。
- 実装した内容は、テストが通るだけでなく、実際に起動して操作して確かめる。確かめていない点は報告に明記する。
- ドキュメントに載せる手順（コマンド）は、実際に試してから載せる。
- htmx の振る舞いは Go の単体テストでは確かめられない。画面を変えたら、ブラウザ（playwright-cli）で実際に操作して確かめる。

### 検証用の実機（simwifi）

aka 版の CLAUDE.md「検証用の実機（simwifi）」と同じ（`ssh simwifi`、ユーザー `claude`、Debian 13、Tailscale `100.126.128.93`、作業は `~/aka-work/` の中だけ、push 前の版は tar を ssh で流して転送、待ち受けは Tailscale のアドレスかループバックに限る、終わったら片付ける）。加えて、本PoCのセッションで分かったこと:

- `claude` の UID / GID は 1001。コンテナでホストのファイル（パーミッション 600 の秘密鍵等）を読む場合は `user: "$(id -u):$(id -g)"` にする（1000 を決め打ちすると読めない）。
- `ssh simwifi 'bash -s' <<'EOF' ... EOF` の中で `docker compose exec -T` / `docker compose run` を使うと、スクリプトの残りを標準入力として読まれて後続のコマンドが実行されない。これらのコマンドには `</dev/null` を付ける。
- 2026-10-08 時点の simwifi は、作業前の状態でイメージ・ボリュームなし、Docker ネットワークは既定の 3 つだけ。片付けでは `docker image prune -af` と `docker builder prune -af` まで行ってよいが、作業前に `docker images` を記録して確かめる。
- 本PoCを simwifi で起動するときは、`deployments/docker-compose.override.yml` に `ports: !override` を書いて auth-server / acct-server の 1812/1813 もループバックに限る（本PoCの作業ツリーを転送し、`.env` に `VALKEY_PASSWORD`・`PROVISIONING_API_ADMIN_CLIENTS` を書き、`certs/provisioning/` にサーバー証明書を置いて `docker compose --profile provisioning up -d --build`）。手順の詳細は本PoCの T-03 G11。
- 本PoCの Admin TUI を相手に相互参照を確かめるときは、手元の tmux から `ssh -t simwifi` で起動して `tmux capture-pane` で画面を読める（本PoCのセッションで使った方法）。

## 本PoC側の変更が要るとき

- Provisioning API の変更が必要になったら、本PoC側の OpenAPI・D-13・実装・テストも合わせて直す。本PoCのリポジトリのルールは本PoCの CLAUDE.md に従う（`make build` / `make test` / `make lint`、Go Workspace のため `./...` は使えない、ブランチは feature → develop を squash、develop → main をマージコミット）。
- 本PoCのドキュメントは、改版するとファイル名の版数も上げる（`git mv`）。末尾の改版履歴に行を足し、ドキュメント一覧も改版する。`docs/D-04_ログ仕様設計書_*.md` だけは改行が CRLF なので、スクリプトで書き換えるときは改行を保つ。

## 進め方

- コミットは依頼されたときだけ行う。
- 設計上の判断が要る点は、番号つきの確認事項として提示し、推奨案を添える。
- 実装が設計ドキュメントとずれた場合は、ドキュメントも同じ作業の中で更新する。
