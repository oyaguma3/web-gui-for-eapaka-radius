# web-gui-for-eapaka-radius

[![CI](https://github.com/oyaguma3/web-gui-for-eapaka-radius/actions/workflows/ci.yml/badge.svg)](https://github.com/oyaguma3/web-gui-for-eapaka-radius/actions/workflows/ci.yml)

[EAP-AKA RADIUS PoC](https://github.com/oyaguma3/eapaka-radius-server-poc)（以下「本PoC」）の管理 GUI（BFF + Web GUI。コマンド名 `eapaka-webgui`）です。本PoCの Provisioning API（provisioning-api）だけを使って、加入者、RADIUSクライアント、認可ポリシーを管理します。加入者のデータや鍵情報は自分では保存しません。

```
[ブラウザ] ──HTTPS（VPN 越し）──> BFF + Web GUI ──mTLS（X-Operator-Id）──> provisioning-api（本PoC） ──> Valkey（本PoC）
                                      │
                                Valkey（BFF 専用）
```

個人利用・検証用途の GUI です。インターネットに直接公開せず、VPN（Tailscale、WireGuard など）越しに使うことを前提にしています。aka-only-server の管理 GUI（[web-gui-for-aka-only-server](https://github.com/oyaguma3/web-gui-for-aka-only-server)）と作りをそろえていて、同じホストで並べて動かせます。

## 特徴

- アカウントは 3 種類（最初の管理者、管理者、一般ユーザー）。権限は BFF で判定し、操作者を provisioning-api の監査ログに残します。
- Ki / OPc と RADIUSクライアントの共有シークレットは、管理者がボタンを押したときだけ取得して表示します。一般ユーザーが Ki / OPc を扱えるのは、加入者の登録フォームの入力中だけです。
- 認可ポリシーは加入者とは別に扱い、ルールの追加・削除・並べ替えを画面で行って、保存で全体を置き換えます。
- BFF の操作と provisioning-api の記録を、トレースID（`X-Trace-ID`）で突き合わせられます。
- パスワードは argon2id で保存し、ログイン試行の回数を制限します。セッションは無操作のタイムアウトとログインからの最大時間で切れます。
- ブラウザ向けの HTTPS は BFF 自身で終端します。証明書は自己署名（自動生成）か持ち込み（`tailscale cert` など。更新は再起動なしで反映）です。
- 画面は Go の `html/template` と htmx、Pico CSS で作っています。JavaScript は書いていません。

| 画面 | 内容 |
|---|---|
| ダッシュボード | ノード名、provisioning-api のバージョンと起動日時、加入者・RADIUSクライアント・認可ポリシーの件数 |
| 加入者 | 一覧（IMSI の前方一致）、登録、詳細、削除。Ki / OPc の表示と変更、SQN / AMF の変更（管理者） |
| RADIUSクライアント | 一覧、詳細。登録、変更、削除、共有シークレットの表示（管理者） |
| 認可ポリシー | 一覧（IMSI の前方一致）、作成・編集（ルールの追加・削除・並べ替え）、削除 |
| 監査ログ | BFF の監査ログ（ログイン、アカウント、BFF を通した Provisioning API の操作）（管理者） |
| アカウント | アカウントの作成・削除・パスワード再設定（管理者） |

## 導入

Docker Engine と compose プラグインが必要です。本PoCで provisioning-api を有効にし、BFF を管理クライアントとして登録します。手順は[運用ガイド](docs/operation-guide.md)の 2 章にあります。

流れは次の通りです。

1. 本PoC側で provisioning-api のサーバー証明書を作る（SAN に `DNS:provisioning-api`）。
2. `.env.example` を `.env` にコピーし、Valkey のパスワード、最初の管理者、公開するアドレスを書く。
3. `eapaka-webgui gen-client-cert` で BFF のクライアント証明書を作り、表示された値を本PoCの `.env` の `PROVISIONING_API_ADMIN_CLIENTS` に登録して、provisioning-api を起動する。
4. 2 つの証明書を `certs/` に置く（所有者は UID 65532）。
5. 起動して、provisioning-api に接続できることを確かめる。

```bash
docker compose up -d
```

```bash
docker compose exec eapaka-webgui /eapaka-webgui check-admin
```

6. ブラウザで開き、最初の管理者でログインして、日常の操作に使うアカウントを作る。

## ドキュメント

| ファイル | 内容 |
|---|---|
| [docs/operation-guide.md](docs/operation-guide.md) | 導入（同一ホスト / 別ホスト）、ブラウザ向け HTTPS（自己署名 / Tailscale）、公開範囲、アカウントの運用、ログと監査ログ、バックアップ、更新、障害時の確認、環境変数 |
| [docs/design-overview.md](docs/design-overview.md) | 設計概要（アカウントと権限、セッション、Provisioning API との連携、データモデル、今後の計画） |
| [docs/screen-spec.md](docs/screen-spec.md) | 画面仕様と権限ごとの表示差 |

## 開発

Go 1.27 以上が必要です。外部依存は `github.com/valkey-io/valkey-go` と `golang.org/x/crypto` だけです。

```bash
go test ./...
```

Valkey を使う結合テストと、本PoCの provisioning-api を相手にした契約テストは、接続先を環境変数で指定したときだけ動きます。Valkey の結合テストは論理データベース 1 番の全データを消すので、専用の Valkey を使ってください。

```bash
EAPAKA_WEBGUI_TEST_VALKEY_ADDR=127.0.0.1:16380 EAPAKA_WEBGUI_TEST_VALKEY_PASSWORD=<パスワード> go test ./internal/store/
```

```bash
EAPAKA_WEBGUI_TEST_ADMIN_URL=https://<host>:9444/admin/v1 EAPAKA_WEBGUI_TEST_ADMIN_CLIENT_CERT=<bff.pem> EAPAKA_WEBGUI_TEST_ADMIN_SERVER_CERT=<server.pem> go test -run Integration ./internal/provapi/
```

契約テストは、テスト用の加入者・認可ポリシー（IMSI が `00101` で始まるもの）と RADIUSクライアント（`198.51.100.0/24` のアドレス）を作り、終わったら削除します。`EAPAKA_WEBGUI_TEST_ADMIN_LOG` に provisioning-api の標準出力を書いたファイルを指定すると、監査ログの操作者・管理クライアント・トレースID も確かめます。

GitHub Actions（`.github/workflows/ci.yml`）で、push と pull request のたびに次を実行します。

| ジョブ | 内容 |
|---|---|
| テスト | gofmt の確認、`go vet`、race 検出つきのテスト（Valkey の結合テストを含む） |
| イメージと compose の設定 | Docker イメージのビルド、compose の設定の検査（同一ホスト・別ホスト） |
| Provisioning API との契約テスト | 本PoCを固定のコミット（ci.yml の `POC_REF`）で取得して provisioning-api をビルド・起動し、`gen-client-cert` で作った証明書を登録して契約テストを実行する |

同梱しているサードパーティのファイル:

| ファイル | 版 | ライセンス |
|---|---|---|
| `internal/web/static/htmx.min.js` | htmx 2.0.11 | 0BSD |
| `internal/web/static/pico.min.css` | Pico CSS 2.1.1 | MIT |

## ライセンス

[LICENSE](LICENSE) を参照してください。
