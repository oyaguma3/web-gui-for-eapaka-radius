# web-gui-for-eapaka-radius 設計概要

- 状態: 設計中（初版。2026-10-08）
- 対象: BFF と Web GUI（コマンド名 `eapaka-webgui`）。
- 関連:
  - 管理対象のシステムと Provisioning API: eapaka-radius-server-poc リポジトリの `docs/D-13_Provisioning_API詳細設計書_r*.md`、`docs/openapi/provisioning-api.yaml`
  - 手本にする実装: web-gui-for-aka-only-server（以下「aka 版」）の `docs/design-overview.md`。作法は aka 版に揃え、本書では違う点を中心に書く。

## 1. 位置づけ

EAP-AKA RADIUS PoC（eapaka-radius-server-poc。以下「本PoC」）の管理 GUI。本PoCの Provisioning API（provisioning-api）だけを使い、加入者・RADIUSクライアント・認可ポリシーを操作する。本PoCの Valkey には直接触れず、加入者データや鍵情報を自分では保存しない。

```
[ブラウザ] ──HTTPS（VPN 越し）──> BFF + Web GUI ──mTLS（X-Operator-Id）──> provisioning-api（本PoC） ──> Valkey（本PoC）
                                      │
                                Valkey（BFF 専用）
```

- 本PoCからは独立したリポジトリとし、本PoCの外に置く BFF として動く（D-13 §1.2）。同一ホストでも別ホストでも動かせる。
- BFF は Go で実装し、HTML を返す（HTMX + Pico CSS）。ログインアカウント、権限、セッションはこちらで持つ。
- 1つの BFF が扱うのは本PoCの1ノード（provisioning-api 1つ）とする。複数ノードや aka-only-server をあわせて扱うのは、将来の統合API（仮称 eapaka-node-provisioner、別リポジトリ）の役目とする（§10）。
- Provisioning API の呼び出しはインターフェースの後ろに隠し、将来 eapaka-node-provisioner に付け替えやすくしておく。
- 本PoCの Admin TUI とは、原則として同時に使わない（D-13 §2.3）。

## 2. 技術方針

aka 版と同じ。

- 言語: Go 1.27.1。標準ライブラリを優先する（`net/http` のメソッドつき ServeMux、`html/template`、`embed`、`log/slog`、`encoding/json/v2`）。
- HTMX と Pico CSS はファイルをリポジトリに同梱して配信する。Alpine.js は使わない。
- 外部依存は `github.com/valkey-io/valkey-go`（BFF 専用 Valkey）と `golang.org/x/crypto`（argon2id）に限る。
- module パス: `github.com/oyaguma3/web-gui-for-eapaka-radius`
- コマンド名は `eapaka-webgui`。設定は環境変数だけで受け取り、名前の接頭辞は `EAPAKA_WEBGUI_` とする（aka 版の `WEBGUI_` と区別する）。
- 配備: Docker Compose（BFF + 専用 Valkey）。本PoCと同一ホストなら共有ネットワーク用の compose ファイルを重ね、別ホストなら `compose.yaml` だけで動かす（`.env` の `COMPOSE_FILE` で選ぶ。§7.3）。
- 公開ポートの既定は `127.0.0.1:8445`。aka 版（`127.0.0.1:8444`）と同じホストで並べて動かせるようにする。
- aka 版の認証・セッション・証明書・描画まわりのコードは、コピーして手直しする。共有モジュールへの切り出しは、両方が安定してから検討する。

## 3. ブラウザ向けの HTTPS とネットワーク

aka 版 §3 と同じ。

- GUI はインターネットに直接公開せず、VPN 越しのアクセスを基本とする。HTTPS は BFF 自身で終端する（リバースプロキシは置かない）。
- サーバー証明書は自己署名（なければ生成）または持ち込み（`tailscale cert` 等）。ファイルの変更は再起動なしで読み直す。
- TLS 1.2 以上、HTTP/2 に対応する。HSTS は付けない。
- `ports` のバインド先を VPN 側のアドレスに限定する（Docker が公開したポートは ufw の規則を通らない）。

## 4. アカウントと権限

### 4.1 アカウント種別

aka 版 §4.1 と同じ（最初の管理者・管理者・一般ユーザー。ユーザーID は `X-Operator-Id` の形式 `^[A-Za-z0-9._@-]{1,64}$`、パスワードの規則、他人が設定したパスワードでのログイン後の変更の強制）。

### 4.2 操作権限

| 操作 | 最初の管理者 | 管理者 | 一般ユーザー |
|---|---|---|---|
| ダッシュボードの閲覧 | ○ | ○ | ○ |
| 加入者の一覧・閲覧（Ki / OPc 以外） | ○ | ○ | ○ |
| 加入者の登録（Ki / OPc / SQN / AMF を入力） | ○ | ○ | ○ |
| 登録済み加入者の Ki / OPc の閲覧・変更 | ○ | ○ | × |
| 登録済み加入者の SQN / AMF の変更 | ○ | ○ | × |
| 加入者の削除 | ○ | ○ | ○ |
| 認可ポリシーの一覧・閲覧 | ○ | ○ | ○ |
| 認可ポリシーの作成・変更・削除 | ○ | ○ | ○ |
| RADIUSクライアントの一覧・閲覧（共有シークレット以外） | ○ | ○ | ○ |
| RADIUSクライアントの登録・変更・削除 | ○ | ○ | × |
| RADIUSクライアントの共有シークレットの閲覧 | ○ | ○ | × |
| BFF の監査ログの閲覧 | ○ | ○ | × |
| 一般ユーザーアカウントの作成・削除・パスワード再設定 | ○ | ○ | × |
| 管理者アカウントの作成・削除・パスワード再設定 | ○ | × | × |
| 自分のパスワード変更 | ×（`.env`） | ○ | ○ |

- 一般ユーザーは、加入者の登録フォームの入力中に限り Ki / OPc / SQN / AMF を扱える。登録が完了した後は Ki / OPc を表示しない。
- 一般ユーザーは加入者を削除して再登録することで、SQN / AMF を実質的に変更できる。これは許容し、登録・削除を操作者つきで provisioning-api の監査ログに残すことで追跡できるようにする（aka 版と同じ）。
- RADIUSクライアントの変更は、AP からの RADIUS を受け付けるかどうかに直結するため、管理者だけが行える。
- 権限判定は BFF だけで行う。provisioning-api は BFF を全権の管理クライアントとして扱う（D-13 §5）。

## 5. セッションとセキュリティ

aka 版 §5 と同じ（argon2id、サーバー側セッションと `__Host-` Cookie、無操作 30 分・最大 12 時間、スタンプによる無効化、`CrossOriginProtection`、ログイン試行回数の制限、`Cache-Control: no-store`、CSP、htmx の履歴キャッシュを使わない、BFF の監査ログ）。

加えて、本PoC固有の秘密の値の扱い:

- Ki / OPc と RADIUSクライアントの共有シークレットは、詳細画面のボタンを押したときだけ provisioning-api から取得して表示する（`GET /subscribers/{imsi}/keys`、`GET /clients/{ip}/secret`。取得は provisioning-api の監査ログに残る）。他の操作や画面の移動で表示を消す。
- これらの値は BFF のログに出さない。provisioning-api のリクエスト・レスポンスのボディはログに出さず、アクセスログにはクエリ文字列を出さない。

## 6. 画面

| 画面 | 内容 | Provisioning API |
|---|---|---|
| ログイン / パスワード変更 / アカウント管理 | aka 版と同じ | - |
| ダッシュボード | ノード名、provisioning-api のバージョンと起動日時、加入者・RADIUSクライアント・認可ポリシーの件数 | `GET /status` |
| 加入者一覧 | 50 件ずつのページング（`cursor` / `nextCursor`）、IMSI の前方一致検索、総数 | `GET /subscribers` |
| 加入者登録 | IMSI、Ki、OPc、AMF（既定 8000）、SQN（既定 000000000000） | `POST /subscribers` |
| 加入者詳細・編集 | AMF / SQN の表示。管理者は Ki / OPc の表示（ボタン）と変更、SQN / AMF の変更（変わった項目だけを送る）。削除。同じ IMSI の認可ポリシーの有無と、ポリシーの画面への移動 | `GET` / `PATCH` / `DELETE /subscribers/{imsi}`、`GET /subscribers/{imsi}/keys`、`GET /policies/{imsi}` |
| RADIUSクライアント一覧・登録 | 全件（IP アドレスの順）。管理者は登録（IP、共有シークレット、名前、ベンダー） | `GET` / `POST /clients` |
| RADIUSクライアント詳細・編集 | 名前・ベンダー。管理者は共有シークレットの表示（ボタン）と変更、名前・ベンダーの変更、削除 | `GET` / `PATCH` / `DELETE /clients/{ip}`、`GET /clients/{ip}/secret` |
| 認可ポリシー一覧 | 50 件ずつのページング、IMSI の前方一致検索、総数。default とルールの件数 | `GET /policies` |
| 認可ポリシー編集 | default（allow / deny）の切り替え、ルールの追加・削除・並べ替え（上から順に評価）。ルールは NAS-ID、許可する SSID（複数）、VLAN ID、Session-Timeout。保存時に全体を置き換える。新規作成（加入者がなくても作れる）と削除 | `GET` / `PUT` / `DELETE /policies/{imsi}` |
| 監査ログ | BFF の監査ログ（provisioning-api の監査ログの参照は §10 の拡張後） | - |

- 認可ポリシーは加入者の下ではなく独立して扱う。接続方式01（aka-only-server）の加入者は鍵を aka-only-server が持つため、本PoCには `sub:{IMSI}` がなく `policy:{IMSI}` だけがある（D-13 §3.3）。
- 認可ポリシーの編集は本 GUI で新しく作る画面で、ルールの行を HTMX で増やす・減らす・上下に動かす。入力の検証は provisioning-api の `invalidParams`（例 `rules[0].allowedSsids[1]`）を各行の項目に対応付けて表示する。
- 本PoCの Admin TUI と同時に使った場合の 404 / 409（他の操作で削除された・既に存在する）は、その旨が分かる文で表示し、一覧を読み直せるようにする。
- 画面の詳細は `docs/screen-spec.md`（後で作成）に書く。

## 7. Provisioning API との連携

### 7.1 接続

- 本PoCの `docs/openapi/provisioning-api.yaml` を契約とする。
- 接続先 URL、BFF のクライアント証明書と秘密鍵、provisioning-api のサーバー証明書（検証用）を設定で与える（`EAPAKA_WEBGUI_ADMIN_URL`、`_ADMIN_CLIENT_CERT`、`_ADMIN_CLIENT_KEY`、`_ADMIN_SERVER_CERT`）。
  - provisioning-api のサーバー証明書を、信頼する証明書として検証に使う（固定）。ホスト名の検証も行うので、接続に使う名前（同一ホストでは `provisioning-api`）が証明書の SAN に入っている必要がある。
  - TLS 1.2 以上、HTTP/2、リダイレクトはたどらない。1 回の呼び出しは 10 秒で打ち切る。
- provisioning-api には aka-only-server の `client gen-cert` に当たる機能がないため、BFF のクライアント証明書は BFF 側で作る。`eapaka-webgui gen-client-cert` で証明書と秘密鍵を生成し、SHA-256 フィンガープリントを表示する。これを本PoCの `.env` の `PROVISIONING_API_ADMIN_CLIENTS` に `名前=フィンガープリント` で登録する（名前は provisioning-api の監査ログの `mgmt_client` になる）。
- 導入時の確認用に `eapaka-webgui check-admin`（provisioning-api に接続して `/status` を取得する）を用意する。起動時にも接続を確かめてログに出す。接続できなくても BFF は起動し、画面に原因の見当を示す（クライアント証明書の未登録、サーバー証明書の不一致や SAN の不足、名前解決や接続の失敗など。aka 版の diagnose を流用）。

### 7.2 呼び出しの作法

- 操作者のユーザーID はリクエストのコンテキストに入れ、API クライアントが `X-Operator-Id` ヘッダーで渡す。変更操作と秘密の値の取得は、操作者が入っていなければ送らない。
- 変更は JSON Merge Patch（`application/merge-patch+json`）で、変わった項目だけを送る。認可ポリシーは PUT で全体を置き換える。
- ProblemDetails の `cause` / `invalidParams` を、HTTP のステータスと日本語の文・項目名に対応付けて表示する（aka 版の対応表に `CLIENT_NOT_FOUND`、`POLICY_NOT_FOUND`、`CLIENT_ALREADY_EXISTS` を加える）。
- 16 進は provisioning-api が小文字で返すので、そのまま表示する。日時は BFF のタイムゾーン（`TZ`）で表示する。

### 7.3 配置と経路

| 配置 | 経路 |
|---|---|
| 本PoCと同一ホスト | 本PoC側が作る共有の Docker ネットワーク（既定名 `eapaka-prov`。本PoCの `.env` の `PROVISIONING_SHARED_NETWORK`）に、BFF（だけ。専用 Valkey は参加させない）が external として参加し、`https://provisioning-api:9444/admin/v1` で接続する。BFF 側は共有ネットワークに参加するための compose ファイルを重ねる |
| 別ホスト | 本PoC側で `PROVISIONING_API_BIND` を VPN（WireGuard / Tailscale 等）のアドレスにし、BFF はそのアドレスで接続する。サーバー証明書の SAN にそのアドレス・名前を入れる |

- 本PoCの provisioning-api は既定で `127.0.0.1:9444` にだけ公開される。しかし、別の compose で動く BFF のコンテナからはホストのループバックに届かないため、同一ホストでは共有ネットワークを使う。本PoC側の共有ネットワークと、サーバー証明書の SAN（`DNS:provisioning-api`）の手順は用意済み（本PoC #45。D-13 r4 §2.2・§8.2、B-02 r21 §15.11）。
- 共有ネットワークは本PoC側を `docker compose --profile provisioning up -d` したときだけ作られる。本PoC側を先に起動する（ネットワークがないと BFF は `network eapaka-prov declared as external, but could not be found` で起動できない）。BFF が参加したまま本PoC側を止めてもネットワークは残り、本PoC側を起動し直せば BFF は再起動なしで接続できる（simwifi で確認済み）。共有ネットワークからは provisioning-api だけが見え、本PoCの Valkey 等には届かない。
- provisioning-api のログの `src_ip` は、共有ネットワーク経由では BFF のコンテナの IP、ポート公開経由ではゲートウェイIPになることがあり、BFF のアドレスとは限らない（本PoC O-05 §11.6）。provisioning-api 側で BFF を識別するのは、クライアント証明書の名前（`mgmt_client`）である。

## 8. データモデル（BFF 専用 Valkey）

aka 版 §8 と同じ（`user:{id}`、`users`、`session:{sha256(sid)}`、`loginfail:{id}`、`audit`）。本PoCの Valkey とは別のインスタンスで、共有ネットワークには参加させない。

## 9. 進め方

aka 版と同じく 5 つのステップに分け、各ステップの終わりに「作ったもの」と「実際に動かして確かめたこと」を報告して確認をもらう。

1. 骨格（module、コマンド、設定、HTTPS サーバー、compose）
2. Provisioning API クライアントと契約テスト（`gen-client-cert`、`check-admin`、diagnose）
3. アカウントとセッション（aka 版からの移植）
4. 画面（ダッシュボード、加入者、RADIUSクライアント、認可ポリシー、監査ログ）
5. compose・運用ガイド・実機（simwifi）での通しの確認

- 単体テストに加え、BFF 専用 Valkey の結合テストと、provisioning-api との契約テスト（テスト用の IMSI・IP を作って最後に消す）を、環境変数で接続先を与えたときだけ動かす（aka 版と同じ）。
- CI で provisioning-api を実際に起動して契約テストを行う方法（本PoCのイメージの取得の仕方）は、ステップ2 で決める。
- htmx の振る舞いは playwright-cli でブラウザを操作して確かめる。

## 10. 今後の計画

| 順序 | 内容 |
|---|---|
| 1 | 本 GUI（eapaka-webgui）の初版（§9） |
| 2 | 本PoCの Provisioning API の拡張（D-13 §10 の候補。監査ログの参照 API、セッション・統計の参照、CSV に相当する一括操作など）と、本 GUI への反映。eapaka-node-provisioner の設計の前に、Provisioning API の作法を確立しておく |
| 3 | eapaka-node-provisioner（本PoCの Provisioning API と aka-only-server の管理API を組み合わせて操作する統合API）の設計 |

初版の範囲外とするもの（2 で検討）:

- provisioning-api の監査ログ・サーバーログの参照（本PoCのホストのログファイル `provisioning-api.log` を参照する）
- セッション・統計の参照（Admin TUI で参照する）
- CSV のインポート・エクスポート（Admin TUI で行う）

## 11. ドキュメント

| ファイル | 内容 |
|---|---|
| `docs/design-overview.md` | 本書 |
| `docs/screen-spec.md` | 画面仕様と権限ごとの表示差（後で作成） |
| `docs/operation-guide.md` | 導入（BFF の登録を含む）、アカウント運用、ブラウザ向け HTTPS、公開範囲、バックアップ、障害時の確認、環境変数（後で作成） |
