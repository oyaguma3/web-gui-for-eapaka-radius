# web-gui-for-eapaka-radius 設計概要

- 状態: 初版（2026-10-08。§9 のステップ 1〜5）と、§10 の 2（本PoCの Provisioning API 0.3.0 の監査ログ・セッションの参照。2026-10-09）を実装済み。§10 の 3（eapaka-node-provisioner）は設計・実装済み（同リポジトリ）。§10 の 4（eapaka-node-provisioner 経由の接続。§12）も実装済み（2026-10-10）。§10 の 5（加入者の停止・再開。§13）も実装済み（2026-10-10）
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
- 接続先は、provisioning-api に直接（既定）と、eapaka-node-provisioner 経由のどちらかを設定で選ぶ（2026-10-10 決定。§12）。provisioner は必須にしない。
- 本PoCの Admin TUI とは、原則として同時に使わない（D-13 §2.3）。

## 2. 技術方針

aka 版と同じ。

- 言語: Go 1.27.1。標準ライブラリを優先する（`net/http` のメソッドつき ServeMux、`html/template`、`embed`、`log/slog`、`encoding/json/v2`）。
- HTMX と Pico CSS はファイルをリポジトリに同梱して配信する。Alpine.js は使わない。
- 外部依存は `github.com/valkey-io/valkey-go`（BFF 専用 Valkey）と `golang.org/x/crypto`（argon2id）に限る。
- module パス: `github.com/oyaguma3/web-gui-for-eapaka-radius`
- コマンド名は `eapaka-webgui`。設定は環境変数だけで受け取り、名前の接頭辞は `EAPAKA_WEBGUI_` とする（aka 版の `WEBGUI_` と区別する）。
- 配備: Docker Compose（BFF + 専用 Valkey）。本PoCと同一ホストなら共有ネットワーク用の compose ファイル（`compose.eapaka-prov.yaml`）を重ね、別ホストなら `compose.yaml` だけで動かす（`.env` の `COMPOSE_FILE` で選ぶ。§7.3）。コンテナは distroless の nonroot（UID 65532）で動かす。
- 公開ポートの既定は `127.0.0.1:8445`。aka 版（`127.0.0.1:8444`）と同じホストで並べて動かせるようにする。BFF の待ち受け（`EAPAKA_WEBGUI_ADDR`）の既定も `:8445` とし、バイナリを直接動かしたときに aka 版（`:8443`）とぶつからないようにする。
- aka 版の認証・セッション・証明書・描画まわりのコードは、コピーして手直しする。共有モジュールへの切り出しは、両方が安定してから検討する。

## 3. ブラウザ向けの HTTPS とネットワーク

aka 版 §3 と同じ。

- GUI はインターネットに直接公開せず、VPN 越しのアクセスを基本とする。HTTPS は BFF 自身で終端する（リバースプロキシは置かない）。
- サーバー証明書は自己署名（どちらのファイルもなければ起動時に生成し、既定では `/data/tls/` のボリュームに保存する。SAN は `EAPAKA_WEBGUI_TLS_HOSTS`）または持ち込み（`tailscale cert` 等）。ファイルの内容の変更は 1 分ごとに確かめ、再起動なしで読み直す。
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
| 加入者の停止・再開（認可ポリシーの状態。§13） | ○ | ○ | ○ |
| RADIUSクライアントの一覧・閲覧（共有シークレット以外） | ○ | ○ | ○ |
| RADIUSクライアントの登録・変更・削除 | ○ | ○ | × |
| RADIUSクライアントの共有シークレットの閲覧 | ○ | ○ | × |
| セッションの一覧（IMSI での絞り込み） | ○ | ○ | ○ |
| 監査ログ（BFF・provisioning-api）の閲覧 | ○ | ○ | × |
| 一般ユーザーアカウントの作成・削除・パスワード再設定 | ○ | ○ | × |
| 管理者アカウントの作成・削除・パスワード再設定 | ○ | × | × |
| 自分のパスワード変更 | ×（`.env`） | ○ | ○ |

- 一般ユーザーは、加入者の登録フォームの入力中に限り Ki / OPc / SQN / AMF を扱える。登録が完了した後は Ki / OPc を表示しない。
- 一般ユーザーは加入者を削除して再登録することで、SQN / AMF を実質的に変更できる。これは許容し、登録・削除を操作者つきで provisioning-api の監査ログに残すことで追跡できるようにする（aka 版と同じ）。
- RADIUSクライアントの変更は、AP からの RADIUS を受け付けるかどうかに直結するため、管理者だけが行える。
- 権限判定は BFF だけで行う。provisioning-api は BFF を全権の管理クライアントとして扱う（D-13 §5）。

## 5. セッションとセキュリティ

aka 版 §5 と同じ（argon2id、サーバー側セッションと `__Host-` Cookie、無操作 30 分・最大 12 時間、スタンプによる無効化、`CrossOriginProtection`、ログイン試行回数の制限、`Cache-Control: no-store`、CSP、htmx の履歴キャッシュを使わない、BFF の監査ログ）。実装は aka 版の `internal/auth`・`internal/store`・画面をコピーして手直しした。

- セッションの Cookie の名前は `__Host-eapaka-webgui-session` とする。Cookie はポートを区別しないので、同じホスト名で aka 版（`__Host-aka-webgui-session`）と並べても互いに上書きしないよう、別の名前にする。
- 最初の管理者のパスワードハッシュのソルトは、ユーザーID と BFF の名前（`eapaka-webgui`）から決める。`.env` のパスワードを変えずに起動し直してもセッションは続き、変えて起動し直すとセッションは無効になる。
- ログイン中のユーザーID は、Provisioning API の操作者（`X-Operator-Id`）としてリクエストのコンテキストに入れる。

加えて、本PoC固有の秘密の値の扱い:

- Ki / OPc と RADIUSクライアントの共有シークレットは、詳細画面のボタンを押したときだけ provisioning-api から取得して表示する（`GET /subscribers/{imsi}/keys`、`GET /clients/{clientId}/secret`。取得は provisioning-api の監査ログに残る）。他の操作や画面の移動で表示を消す。
- これらの値は BFF のログに出さない。provisioning-api のリクエスト・レスポンスのボディはログに出さず、アクセスログにはクエリ文字列を出さない。

BFF の監査ログには、aka 版と同じログイン・アカウントの操作に加えて、BFF を通した Provisioning API の操作（加入者の登録・変更・削除、Ki / OPc の表示、RADIUSクライアントの登録・変更・削除、共有シークレットの表示、認可ポリシーの保存・削除）も記録する（2026-10-08 決定）。

- 初版では provisioning-api の監査ログを画面で参照できなかった（本PoCのホストのログファイルにだけあった）ため、BFF の監査ログの画面で操作を追えるようにした。aka 版は管理API の監査ログを画面で参照できたので、BFF にはログインとアカウントの操作だけを記録していた。
- Provisioning API 0.3.0 で監査ログを参照できるようになった（`GET /audit-logs`。§10）後も、BFF の監査ログへの記録は続ける。BFF の記録には操作元（ブラウザのアドレス）があり、provisioning-api の記録には BFF 以外の管理クライアントの操作も含まれるため、両方を監査ログの画面のタブで見られるようにする（2026-10-09）。
- 記録するのは、操作者、操作元、対象（IMSI、RADIUSクライアントは `#ID`）、内容（変えた項目の名前、IP アドレスの変更前後など。秘密の値は含めない）、トレースID。トレースID で provisioning-api の監査ログの `trace_id` と突き合わせられる。
- 記録は Provisioning API の操作が成功した後に行う（失敗した操作は provisioning-api と同じく記録しない）。

## 6. 画面

| 画面 | 内容 | Provisioning API |
|---|---|---|
| ログイン / パスワード変更 / アカウント管理 | aka 版と同じ | - |
| ダッシュボード | ノード名、provisioning-api のバージョンと起動日時、加入者・RADIUSクライアント・認可ポリシー・セッションの件数（セッションは provisioning-api 0.3.0 以降） | `GET /status` |
| 加入者一覧 | 50 件ずつのページング（`cursor` / `nextCursor`）、IMSI の前方一致検索、総数 | `GET /subscribers` |
| 加入者登録 | IMSI、Ki、OPc、AMF（既定 8000）、SQN（既定 000000000000） | `POST /subscribers` |
| 加入者詳細・編集 | AMF / SQN の表示。管理者は Ki / OPc の表示（ボタン）と変更、SQN / AMF の変更（変わった項目だけを送る）。削除。同じ IMSI の認可ポリシーの有無と、ポリシーの画面への移動 | `GET` / `PATCH` / `DELETE /subscribers/{imsi}`、`GET /subscribers/{imsi}/keys`、`GET /policies/{imsi}` |
| RADIUSクライアント一覧・登録 | 全件（IP アドレスの順）。ID・IP・名前・ベンダーを表示。管理者は登録（IP、共有シークレット、名前、ベンダー。ID はサーバーが採番） | `GET` / `POST /clients` |
| RADIUSクライアント詳細・編集 | ID・IP・名前・ベンダー。管理者は共有シークレットの表示（ボタン）と変更、IP・名前・ベンダーの変更（IP を変えても ID は変わらない）、削除 | `GET` / `PATCH` / `DELETE /clients/{clientId}`、`GET /clients/{clientId}/secret` |
| 認可ポリシー一覧 | 50 件ずつのページング、IMSI の前方一致検索、総数。default とルールの件数 | `GET /policies` |
| 認可ポリシー編集 | default（allow / deny）の切り替え、ルールの追加・削除・並べ替え（上から順に評価）。ルールは NAS-ID、許可する SSID（複数）、VLAN ID、Session-Timeout。保存時に全体を置き換える。新規作成（加入者がなくても作れる）と削除 | `GET` / `PUT` / `DELETE /policies/{imsi}` |
| セッション | アクティブセッションを接続開始の新しい順に 100 件まで（総数も表示）。IMSI（15 桁）での絞り込み。接続開始、IMSI、NAS（NAS-Identifier と IP）、端末の IP、通信量、Acct-Session-Id、セッションID。読み出しだけ | `GET /sessions` |
| 監査ログ | タブで切り替える。「BFF」は BFF の監査ログ（ログイン・アカウントの操作と、BFF を通した Provisioning API の操作。§5）。「provisioning-api」は provisioning-api の監査ログ（50 件ずつ「さらに古いものを表示」で続きを読み込む。操作者、管理クライアント、操作、対象、内容、トレースID） | `GET /audit-logs`（provisioning-api のタブ） |

- セッションと provisioning-api の監査ログの画面は Provisioning API 0.3.0（本PoC D-13 r6）の API を使う。0.3.0 より前の provisioning-api を相手にすると、その画面では「対応していません（provisioning-api 0.3.0 以降が必要です）」と出し、ダッシュボードではセッションの件数を出さない（他の画面はそのまま使える）。
- RADIUSクライアントは、サーバー採番の ID（本PoC D-13 r5 / API 0.2.0）で識別し、画面の URL にも ID を使う（例 `/clients/3`）。IP アドレスは変更できる項目として扱う。
- 認可ポリシーは加入者の下ではなく独立して扱う。接続方式01（aka-only-server）の加入者は鍵を aka-only-server が持つため、本PoCには `sub:{IMSI}` がなく `policy:{IMSI}` だけがある（D-13 §3.3）。
- 認可ポリシーの編集は本 GUI で新しく作る画面で、ルールの行を HTMX で増やす・減らす・上下に動かす（2026-10-08 決定）。
  - JavaScript は書かない。編集中の状態はフォームにだけ持ち、ルールの追加・削除・並べ替えと既定の動作の切り替えは、フォーム全体を BFF（`POST /policies/{imsi}/edit`）に送って、操作を施した編集欄を返してもらう。provisioning-api には「保存」のときだけ送り（PUT で全体を置き換える）、それまでは何も変えない。
  - 許可する SSID は 1 行に 1 つ書く（前後の空白と空行は除く）。Admin TUI はカンマ区切りだが、SSID にはカンマも使えるため、行で区切る。
  - 新しく作るときの既定は deny、ルールなし（Admin TUI と同じ）。既定の動作を allow にして保存するときは、確認ダイアログで警告する（Admin TUI と同じ）。
  - 入力は保存の前に BFF でも本PoCと同じ規則で確かめる。provisioning-api の `invalidParams`（例 `rules[0].allowedSsids[1]`）も各行の項目に対応付けて表示する。
- 加入者を削除しても、同じ IMSI の認可ポリシーは自動では削除しない（Provisioning API の加入者の削除と同じ。2026-10-08 決定）。削除の確認ダイアログでそのことを伝え、削除後の一覧で、ポリシーが残っていればそのことと画面へのリンクを出す。2 つの操作をまとめて扱うのは eapaka-node-provisioner の役目（§10）とする。
- 本PoCの Admin TUI と同時に使った場合の 404 / 409（他の操作で削除された・既に存在する）は、その旨が分かる文で表示し、一覧を読み直せるようにする。
- 画面の詳細は `docs/screen-spec.md` に書く（ステップ3 でログイン・パスワード変更・アカウント管理・ダッシュボードを記載。残りはステップ4）。

## 7. Provisioning API との連携

### 7.1 接続

- 本PoCの `docs/openapi/provisioning-api.yaml` を契約とする。
- 接続先 URL、BFF のクライアント証明書と秘密鍵、provisioning-api のサーバー証明書（検証用）を設定で与える（`EAPAKA_WEBGUI_ADMIN_URL`、`_ADMIN_CLIENT_CERT`、`_ADMIN_CLIENT_KEY`、`_ADMIN_SERVER_CERT`）。
  - provisioning-api のサーバー証明書を、信頼する証明書として検証に使う（固定）。ホスト名の検証も行うので、接続に使う名前（同一ホストでは `provisioning-api`）が証明書の SAN に入っている必要がある。
  - TLS 1.2 以上、HTTP/2、リダイレクトはたどらない。1 回の呼び出しは 10 秒で打ち切る。
- provisioning-api には aka-only-server の `client gen-cert` に当たる機能がないため、BFF のクライアント証明書は BFF 側で作る。`eapaka-webgui gen-client-cert -name <識別名> [-days 825] [-out-cert F] [-out-key F]` で証明書と秘密鍵を生成し、SHA-256 フィンガープリントを表示する。これを本PoCの `.env` の `PROVISIONING_API_ADMIN_CLIENTS` に `名前=フィンガープリント` で登録する（名前は provisioning-api の監査ログの `mgmt_client` になる）。
  - 使い方は aka-only-server の `client gen-cert` にそろえる。鍵は ECDSA P-256、証明書はクライアント認証用の自己署名で、CN は識別名（英数字と `.` `_` `-` の 64 文字まで。provisioning-api の識別名の規則と同じ）。
  - 出力先を省略すると、証明書と秘密鍵を続けて標準出力に出す。`docker compose run --rm --no-deps -T eapaka-webgui gen-client-cert -name bff-01 > certs/admin-client.pem` のように、ホストに Go がなくても作れる。BFF の設定 `EAPAKA_WEBGUI_ADMIN_CLIENT_KEY` を省略すると、証明書のファイルから秘密鍵を読む。
  - 標準エラーに、フィンガープリントと本PoCの `.env` に貼れる行（`PROVISIONING_API_ADMIN_CLIENTS=bff-01=…`）を出す。出力先のファイルが既にあればエラーにする（使用中の秘密鍵を上書きしない）。
  - コンテナ（UID 65532）から読めるよう、`certs/` のファイルの所有者を 65532 にする（パーミッション 600 のままホストのユーザーの所有だと、BFF は `permission denied` で起動しない）。
- 導入時の確認用に `eapaka-webgui check-admin`（provisioning-api に接続して `/status` を取得する）を用意する。起動時にも接続を確かめてログに出す。接続できなくても BFF は起動し、画面に原因の見当を示す（クライアント証明書の未登録、サーバー証明書の不一致や SAN の不足、名前解決や接続の失敗など。aka 版の diagnose を流用）。
  - TLS 1.3 では、provisioning-api がクライアント証明書を拒否する前に BFF はリクエストを送り終えているため、拒否のアラートより先に接続のリセットが届くことがある（並行して接続すると、2026-10-09 の手元の確認で 1,920 回に 11 回）。接続のリセットも、クライアント証明書が受け付けられなかった可能性として案内する（2026-10-09）。

### 7.2 呼び出しの作法

- 操作者のユーザーID はリクエストのコンテキストに入れ、API クライアントが `X-Operator-Id` ヘッダーで渡す。変更操作と秘密の値の取得は、操作者が入っていなければ送らない。
- BFF はブラウザのリクエストごとにトレースID（16進32桁）を採番してアクセスログに出し、API クライアントが `X-Trace-ID` ヘッダーで渡す（画面からでない呼び出しでは呼び出しごとに採番する）。provisioning-api はこれをログ（`request completed`）と監査ログの `trace_id` に使うので、BFF の操作と provisioning-api の記録をトレースID で突き合わせられる。API クライアントのエラーには、応答の `X-Trace-ID` を持たせる。
- 変更は JSON Merge Patch（`application/merge-patch+json`）で、変わった項目だけを送る。認可ポリシーは PUT で全体を置き換える。
- ProblemDetails の `cause` / `invalidParams` を、HTTP のステータスと日本語の文・項目名に対応付けて表示する（aka 版の対応表に `CLIENT_NOT_FOUND`、`POLICY_NOT_FOUND`、`CLIENT_ALREADY_EXISTS`（RADIUSクライアントの登録と IP の変更で、同じ IP が既にある）を加える）。
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

パスワードは `.env` の `EAPAKA_WEBGUI_VALKEY_PASSWORD` で与える。aka 版の `VALKEY_PASSWORD` から名前を変えたのは、同じホストで編集する本PoCの `.env` の `VALKEY_PASSWORD`（本PoCの Valkey のパスワード）と取り違えないようにするため。

## 9. 進め方

aka 版と同じく 5 つのステップに分け、各ステップの終わりに「作ったもの」と「実際に動かして確かめたこと」を報告して確認をもらう。

1. 骨格（module、コマンド、設定、HTTPS サーバー、compose）
2. Provisioning API クライアントと契約テスト（`gen-client-cert`、`check-admin`、diagnose）
3. アカウントとセッション（aka 版からの移植）
4. 画面（ダッシュボード、加入者、RADIUSクライアント、認可ポリシー、監査ログ）
5. compose・運用ガイド・実機（simwifi）での通しの確認

- 単体テストに加え、BFF 専用 Valkey の結合テストと、provisioning-api との契約テスト（テスト用の IMSI・IP を作って最後に消す）を、環境変数で接続先を与えたときだけ動かす（aka 版と同じ）。
- GitHub Actions の CI（`.github/workflows/ci.yml`）で、push のたびに整形・vet・テストと、イメージのビルド・compose の設定（同一ホスト / 別ホスト）の確認を行う。
- 契約テスト（`internal/provapi/integration_test.go`）は、`EAPAKA_WEBGUI_TEST_ADMIN_URL` 等で接続先を与えたときだけ動く。`EAPAKA_WEBGUI_TEST_ADMIN_LOG` に provisioning-api の標準出力のファイルを与えると、ログファイルの監査ログの操作者（`admin_user`）・`mgmt_client`・`trace_id` も確かめる。監査ログの参照（`GET /audit-logs`）とセッションの参照（`GET /sessions`）も契約テストで確かめる（セッションは RADIUS の認証をしないため、空の場合の形と検証の誤りだけ）。
- CI の契約テストでは、本PoCを固定のコミット（ci.yml の `POC_REF`。本PoCの main のコミット）で checkout し、`apps/provisioning-api` を `go build` して、Valkey のサービスコンテナと openssl で作った自己署名のサーバー証明書（B-02 §15.2 と同じ手順）で起動する。BFF のクライアント証明書は `gen-client-cert` で作って登録する（導入手順と同じ流れを CI でも通す）。本PoCの compose 全体は起動しない。本PoCの Provisioning API を変えたら、`POC_REF` を更新する。
- htmx の振る舞いは playwright-cli でブラウザを操作して確かめる。

## 10. 今後の計画

| 順序 | 内容 |
|---|---|
| 1 | 本 GUI（eapaka-webgui）の初版（§9） |
| 2 | 本PoCの Provisioning API の拡張（D-13 §10 の候補）と、本 GUI への反映。eapaka-node-provisioner の設計の前に、Provisioning API の作法を確立しておく。**実装済み（2026-10-09）**: 監査ログの参照（`GET /audit-logs`）、セッションの参照（`GET /sessions`、`/status` の `sessionCount`）。Provisioning API 0.3.0（本PoC D-13 r6） |
| 3 | eapaka-node-provisioner（本PoCの Provisioning API と aka-only-server の管理API を組み合わせて操作する統合API）の設計。**実装済み（2026-10-10）**: eapaka-node-provisioner リポジトリ（API 0.2.0） |
| 4 | 本 GUI から eapaka-node-provisioner 経由でも操作できるようにする（§12）。**実装済み（2026-10-10）** |
| 5 | 加入者の停止・再開（本PoCの認可ポリシーの状態。Provisioning API 0.4.0、provisioner API 0.3.0）を、直接・provisioner 経由のどちらでも扱う（§13）。**実装済み（2026-10-10）** |

2 で扱わなかったもの（2026-10-09 決定）:

- CSV のインポート・エクスポート: Admin TUI だけで行い、Provisioning API では扱わない（本PoC D-13 §10）
- セッションの切断、操作者ごとの権限（provisioning-api 側での権限判定）、IPv6
- provisioning-api のサーバーログ（監査ログ以外）の参照: 本PoCのホストのログファイル（`provisioning-api.log`）を参照する

## 11. ドキュメント

| ファイル | 内容 |
|---|---|
| `docs/design-overview.md` | 本書 |
| `docs/screen-spec.md` | 画面仕様と権限ごとの表示差 |
| `docs/operation-guide.md` | 導入（BFF の登録を含む）、アカウント運用、ブラウザ向け HTTPS、公開範囲、バックアップ、障害時の確認、環境変数（手順は検証機で実行して確かめたもの） |

## 12. eapaka-node-provisioner 経由の接続

### 12.1 方針（2026-10-10 決定）

- 接続先を設定で選べるようにする。既定は今までどおり provisioning-api に直接。eapaka-node-provisioner（以下「provisioner」）経由を選んだときだけ provisioner が必要になる（provisioner を必須にする「置き換え」はしない）。
- provisioner 経由では、鍵の置き場所（本PoC / aka-only-server）の違う加入者を同じ画面で扱い、加入者の作成・削除で認可ポリシーも一緒に扱う。2 つのノードの食い違い（`issues`）と、操作の記録（`/operations`）を画面に出す。
- 1 つの BFF が扱うのは、引き続き 1 つの接続先（provisioning-api 1 つ、または provisioner 1 つ）とする。

```
直接（既定）:      BFF ──mTLS──> provisioning-api（本PoC）
provisioner 経由:  BFF ──mTLS──> provisioner ──> provisioning-api（本PoC）
                                              └─> aka-only-server の管理API
```

### 12.2 provisioner の API と今の BFF の違い

provisioner の API（provisioner の `docs/openapi/provisioner-api.yaml` 0.2.0）は、中継する部分を provisioning-api と同じ形にしてある。

| 部分 | provisioner の API | BFF の扱い |
|---|---|---|
| RADIUSクライアント（`/clients`）、認可ポリシー（`/policies`）、セッション（`/sessions`） | provisioning-api と同じ形で中継 | 今の API クライアント（`internal/provapi`）を、接続先を provisioner にしてそのまま使う |
| 鍵の取得（`/subscribers/{imsi}/keys`） | 同じ形（`ki`、`opc`） | 同上 |
| 加入者（`/subscribers`） | 統合リソース（`keyStore`、`key`、`policy`、`issues`）。作成はポリシーが必須、削除はポリシーも消す。一覧に総数がない | provisioner 用のクライアントと画面の分岐を加える |
| 状態（`/status`） | 下流 2 つへの接続、PLMN マップ、AVクライアント、未完了の操作の件数 | ダッシュボードを分岐する |
| 監査ログ | provisioner 自身（`/audit-logs`）、provisioning-api（`/prov/audit-logs`）、aka-only-server（`/aka/audit-logs`） | 監査ログのタブを分岐する |
| 操作の記録（`/operations`） | 一覧、取得、`retry`、`dismiss` | 新しい画面 |
| エラー | `OPERATION_IN_PROGRESS`、`OPERATION_UNRESOLVED`、`OPERATION_INCOMPLETE`、`DOWNSTREAM_UNAVAILABLE`、`DOWNSTREAM_ERROR`、`KEY_STORE_MISMATCH` などが増える | 日本語の文の対応表に加える |

### 12.3 確認事項

| # | 事項 | 推奨案 |
|---|---|---|
| 1 | 接続先の選び方 | 環境変数 `EAPAKA_WEBGUI_ADMIN_API`（`provisioning-api`（既定）/ `provisioner`）で選ぶ。接続先の URL・証明書は今の変数（`EAPAKA_WEBGUI_ADMIN_URL` 等）を共用する。起動時と `check-admin` で `/status` の形を見て、設定と違う相手（provisioner の設定なのに provisioning-api につながった等）なら、その旨を出す |
| 2 | 加入者の登録フォーム（provisioner 経由では認可ポリシーが必須） | フォームに認可ポリシーの既定の動作（allow / deny。既定は deny、ルールなし）だけを加える。ルールは登録後に認可ポリシーの画面で編集する（登録後の画面にリンクを出す）。allow を選んだときは、ポリシーの画面と同じく確認ダイアログで警告する |
| 3 | 鍵の置き場所と aka-only-server の加入者の項目 | 置き場所は provisioner が PLMN マップで決める（フォームでは選ばせない）。登録フォームには、ダッシュボードと同じ PLMN マップを表示する。aka-only-server だけの項目（SQN の増加タイプ、平文HTTP の許可）は登録では指定せず aka-only-server の既定（`inc32`、許可しない）にし、詳細画面で管理者が変えられるようにする（SQN / AMF と同じ扱い）。許可するクライアントID は表示だけ |
| 4 | 加入者の詳細・一覧の表示 | 詳細に置き場所、鍵の属性（aka-only-server ならその項目も）、認可ポリシーの概要（default とルールの件数。編集はポリシーの画面）、`issues`（日本語の説明と対処）を出す。一覧に置き場所と `issues` の列を加え、総数は出さない（provisioner が返さない） |
| 5 | 加入者の削除 | provisioner は認可ポリシーも消すので、確認ダイアログの文を変え、削除後の「ポリシーが残っています」の案内は出さない。権限は今と同じ（一般ユーザーも削除できる） |
| 6 | 操作の記録の画面 | 「操作の記録」の画面を加える（未完了の一覧、詳細の手順ごとの状態とエラー）。閲覧は全員、`retry` と `dismiss` は管理者だけ（下流の状態を確かめて判断するため）。ダッシュボードに未完了の件数とリンクを出す。500 `OPERATION_INCOMPLETE` と 409 `OPERATION_UNRESOLVED` のエラーの表示に、その操作へのリンクを付ける |
| 7 | ダッシュボード | provisioner の版と起動日時、下流 2 つの接続の状態（版、ノード名、加入者数、接続できない場合は原因の見当）、vector-gateway の AVクライアント、PLMN マップ、未完了の操作の件数を出す。RADIUSクライアント・認可ポリシー・セッションの件数は provisioner の `/status` にないので出さない |
| 8 | 監査ログのタブ | 「BFF」「provisioner」「provisioning-api」「aka-only-server」の 4 つにする（aka-only-server を使わない設定なら 3 つ）。aka-only-server の監査ログの内容（`detail` のオブジェクト）は「項目: 変更前 → 変更後」の形で表示する |
| 9 | 呼び出しのタイムアウト | provisioner 経由のときは 1 回の呼び出しの上限を 60 秒にする（provisioner の加入者の作成は、補償を含めて下流を最大 5 回呼ぶ。provisioner のロックの有効期限と同じ）。直接のときは今の 10 秒のまま |
| 10 | 二重送信（`Idempotency-Key`） | provisioner 経由のとき、加入者の登録・変更・削除のフォームに、表示のたびに作る乱数のキーを隠し項目で持たせ、`Idempotency-Key` として送る。応答を待たずに送り直した・タイムアウトの後に送り直した場合でも、二重に処理されない（最初の結果が返る） |
| 11 | クライアント証明書と導入 | `gen-client-cert` の標準エラーに、`PROVISIONER_ADMIN_CLIENTS=` の行も出す。同一ホストの provisioner には、provisioner が作る共有ネットワーク（既定名 `eapaka-provisioner`）に参加して `https://eapaka-provisioner:9446/admin/v1` で接続する。そのための compose ファイル（`compose.eapaka-provisioner.yaml`。変数 `PROVISIONER_SHARED_NETWORK`）を加え、`COMPOSE_FILE` で選ぶ |
| 12 | 契約テストと CI | provisioner を相手にした契約テストを加え、CI でも、provisioner・本PoCの provisioning-api・aka-only-server を固定のコミット（`PROVISIONER_REF` / `POC_REF` / `AKA_REF`）でビルド・起動して実行する（provisioner の CI と同じ作り） |
| 13 | BFF の監査ログ | provisioner 経由の操作も今と同じく記録する。加入者の操作の内容に置き場所と操作の記録の ID を加え、`retry` / `dismiss` も記録する |

確認事項は 2026-10-10 にすべて推奨案で合意した。

### 12.4 進め方

1. 設定、provisioner 用の API クライアント（加入者・状態・操作の記録・監査ログ）、`check-admin` と `gen-client-cert` の対応、契約テストと CI … 実装済み（2026-10-10。§12.5）
2. 画面（ダッシュボード、加入者、操作の記録、監査ログのタブ、エラーの文） … 実装済み（2026-10-10。§12.6。画面は `docs/screen-spec.md` §11）
3. compose・運用ガイド・README・画面仕様、simwifi での通しの確認（BFF → provisioner → 本PoC と aka-only-server、eapaka_test での認証） … 完了（2026-10-10）。同一ホスト用の `compose.eapaka-provisioner.yaml`、運用ガイド 11 章。simwifi で、直接接続で動いている BFF を provisioner 経由に切り替え、手元のブラウザ（Tailscale 越し）から RADIUSクライアントと 2 つの置き場所の加入者を登録して eapaka_test で認証（両方 Accept）、認可ポリシーの deny と削除で Reject、操作の記録（409 とリンク、やり直し）、監査ログの 4 つのタブとトレースID の一致を確かめた。別ホストの構成（`COMPOSE_FILE=compose.yaml`、provisioner を Tailscale のアドレスで公開）と、直接接続に戻す手順も確かめた

### 12.5 実装の作り（ステップ 1）

- 設定: `EAPAKA_WEBGUI_ADMIN_API`（`provisioning-api` / `provisioner`）。`EAPAKA_WEBGUI_ADMIN_URL` が空なら、種類ごとの同一ホストの既定（`https://provisioning-api:9444/admin/v1` / `https://eapaka-provisioner:9446/admin/v1`）を使う。1 回の呼び出しの上限は provisioning-api が 10 秒、provisioner が 60 秒。compose は URL の既定を持たず、設定に任せる。
- API クライアント:
  - `internal/provapi` はそのまま provisioner にも使う（接続先を provisioner にした `provapi.Client`）。provisioner が中継する RADIUSクライアント・認可ポリシー・セッション・鍵の取得は、このクライアントのメソッドで呼ぶ。加えたもの: ログとエラーに出す API の名前（`Options.Name`）、`Idempotency-Key`、ほかのパッケージから任意の呼び出しを行う `Call`（`Request`）、ProblemDetails の provisioner の拡張項目（`downstream`、`operationId`、`rolledBack`、`conflicts` 等）。応答の未知の項目は無視して読む（`encoding/json/v2` の既定）。
  - `internal/pvapi` は provisioner だけの部分（状態、加入者の統合操作、操作の記録、3 種類の監査ログ）を、`provapi.Client.Call` で呼ぶ。接続できないときの原因の見当（`pvapi.Diagnose`）は、provapi と同じ判定で、文面を provisioner 向けにした。
  - `internal/provapi/provapitest` は、テスト用の mTLS のサーバーとクライアント（`pvapi` の単体テストが使う）。
- 接続先の取り違えの検出: 直接のときは `/status` の形（provisioner には `downstreams`、provisioning-api には `nodeName` がある）で見分ける。provisioner 経由のときは、provisioner の `/status` が下流の確認で時間がかかることがある（下流が止まっていると下流ごとの上限まで）ため、`/status` を 1 回だけ呼び、`downstreams.prov.configured` が偽なら provisioner でないとみなす。
- `check-admin`: provisioner 経由のときは、provisioner の版、下流 2 つへの接続（接続できなければ provisioner が返す原因の見当）、vector-gateway の AVクライアント、PLMN マップ、未完了の操作の件数を表示する。provisioner に接続できない・取り違えはエラー（終了コード 1）、provisioner の先の下流に接続できないことは表示だけにする（provisioner の `check-downstream` で確かめる）。
- `gen-client-cert`: 標準エラーに `PROVISIONING_API_ADMIN_CLIENTS=` と `PROVISIONER_ADMIN_CLIENTS=` の両方の行を出す。
- 契約テスト（`internal/pvapi/integration_test.go`）: `EAPAKA_WEBGUI_TEST_PROVISIONER_URL` / `_CLIENT_CERT` / `_SERVER_CERT` を指定したときだけ動く。本PoCに鍵を置く加入者（`00101...`）の作成・`Idempotency-Key` の再送・重複の 409（`conflicts`）・取得・一覧・変更・鍵の取得（中継）・削除（ポリシーも消える）、provisioner と provisioning-api の監査ログのトレースID、aka-only-server に鍵を置く加入者（`00102...`。provisioner の PLMN マップで `01` にしたときだけ）の作成・変更・削除と aka-only-server の監査ログ、操作の記録（ないものは 404）、中継（RADIUSクライアント・認可ポリシー・セッション）を確かめる。CI のジョブ「eapaka-node-provisioner との契約テスト」で、provisioner・provisioning-api・aka-only-server を固定のコミットでビルド・起動して実行する。
- 画面はステップ 2 で対応した（§12.6）。

### 12.6 実装の作り（ステップ 2）

- 画面の切り替え: `web.Options.PV`（`pvapi.Client`）を渡すと provisioner 経由の画面になる。加入者（一覧・登録・詳細・変更・削除）と操作の記録は provisioner 用のハンドラーとテンプレート（`subscriber_pv.go`、`operation.go`、`*_pv.html`、`operations.html`、`operation.html`）を使い、ダッシュボード・監査ログ・認可ポリシーの画面は同じハンドラーの中で分岐する。RADIUSクライアント・セッション・鍵の取得は直接のときと同じ。
- 複数の画面で使う部品（Ki / OPc の表示、操作の記録へのリンク、監査ログのタブ、PLMN マップ）は `templates/partials/` に置き、すべてのページから使えるようにした。
- エラーの文は `Handler` のメソッドにし（接続先の名前を変えるため）、provisioner の cause と、操作の記録の ID（`apiFailure.OperationID`。画面に「操作の記録を開く」のリンクを出す）を扱う。
- provisioner の状態の写し: 登録フォームの PLMN マップと、監査ログのタブ（aka-only-server を扱うか）は、provisioner の `/status` の写しを 1 分使い回す（`/status` は下流 2 つを確かめるので、下流が止まっていると時間がかかる）。ダッシュボードは毎回取り直す。
- 登録フォームの allow の確認: 既定の動作を切り替えるとフォームを描き直し（`POST /subscribers/new/form`。provisioner には送らない）、allow のときだけフォームに確認ダイアログを付ける（認可ポリシーの画面と同じ作り）。
- `Idempotency-Key`: フォームを描くたびに `crypto/rand.Text` でキーを作って隠し項目に持たせる。provisioner に届かなかったときはキーを残し、provisioner が 4xx を返したときは新しいキーにする（provisioner は 4xx の応答を覚えるので、入力を直して同じキーで送ると内容の不一致になるため）。
- BFF の監査ログ（§12.3 の 13）: provisioner は成功の応答に操作の記録の ID を返さないので、BFF の監査ログには ID を入れず、鍵の置き場所（`keyStore`）を入れる。provisioner の監査ログとはトレースID で突き合わせられ、そちらに操作の記録の ID がある。操作の記録のやり直し・閉じるは、対象を操作の ID として記録する。
- 応答の時間の上限: provisioner 経由では 1 回の呼び出しの上限を 60 秒にしたので、BFF の HTTPS サーバーの `WriteTimeout`（要求を受けてから応答を書き終えるまで。既定 30 秒）を、呼び出しの上限＋30 秒にした（30 秒を超えた応答が書き出せず、ブラウザに HTTP/2 のエラーとして届くことを手元の確認で見つけた）。あわせて、接続先への接続の確立は 10 秒で打ち切る（接続できない相手を、呼び出し全体の上限まで待たない）。


## 13. 加入者の停止・再開

外部 OSS/BSS のサンプル実装（eapaka-ossbss-sample。同リポジトリの設計概要 §8・§15）のため、本PoCの認可ポリシーに状態（`status`: `active` / `suspended`）が加わった（Provisioning API 0.4.0。本PoC `dbd97f5`）。停止中の加入者の認証は、鍵の置き場所によらず Auth Server が拒否する。変更は専用の `PUT /policies/{imsi}/status` で行い、認可ポリシーの PUT（全体の置き換え）では状態は変わらない。eapaka-node-provisioner も同じパスで中継し（API 0.3.0。`e96390d`）、加入者の統合リソースに `status` を加えた。同じ本PoCを操作する GUI で状態が見えないと混乱するため、本 GUI も直接・provisioner 経由のどちらでも状態の表示と停止・再開を扱う（eapaka-ossbss-sample の設計概要 §15 の 5）。

### 13.1 決定事項（2026-10-10。確認事項 1〜7 をすべて推奨案で合意）

| # | 事項 | 決定 |
|---|---|---|
| 1 | 使える人 | 全員（一般ユーザーを含む）。一般ユーザーも認可ポリシーの変更・削除（認証を拒否する操作）ができるため、それと揃える |
| 2 | 操作の場所 | 認可ポリシーの編集画面と、加入者の詳細画面（直接・provisioner 経由）。状態の印は認可ポリシーにあるが、利用者には加入者の操作に見えるため。一覧からの一括の操作は作らない |
| 3 | 編集中のルールとの関係 | 認可ポリシーの画面で停止・再開しても、保存していないルールの変更は残す（編集中のフォームを一緒に送り、画面を返すときに戻す。ルールは保存しない） |
| 4 | `Idempotency-Key` | 送らない。同じ状態にする操作は何度送っても結果が同じで、認可ポリシーの PUT でも送っていないため |
| 5 | 状態が分からないとき | 状態のない応答（0.3.0 以前の provisioning-api、0.2.0 以前の provisioner）では、状態の欄に「接続先が停止・再開に対応していません」と出し、ボタンを出さない。Valkey を直接書き換えた不正な値は「不明な状態（値）」と出し、停止・再開の両方のボタンを出す（Auth Server はその加入者の認証を拒否する） |
| 6 | ダッシュボード | 停止中の件数は出さない（本PoC・provisioner とも件数を返さない） |
| 7 | 直接接続の加入者の一覧 | 状態を出さない（一覧は認可ポリシーを取得していないため。1 件ごとに取得すると重い）。加入者の詳細と、認可ポリシーの一覧・編集で出す。provisioner 経由の加入者の一覧では出す（provisioner が返す） |

### 13.2 実装の作り

- `provapi.Client.SetPolicyStatus`（`PUT /policies/{imsi}/status`、操作者が必要）を、直接・provisioner 経由の両方で使う（provisioner は Provisioning API と同じ形で中継する）。`provapi.Policy.Status` と `pvapi.Subscriber.Status` に状態が入る。
- 画面: `POST /policies/{imsi}/status`（認可ポリシーの画面。編集中のフォームと、隠し項目 `state`（保存済みの状態）・`dirty`（保存していない変更があるか）を一緒に受け取る）と、`POST /subscribers/{imsi}/status`（加入者の詳細。接続先によって返す画面が違う）。どちらも本文の `status`（`active` / `suspended`）を送り、確認ダイアログを出す。状態とボタンは共通の部品 `policy-status`（`templates/partials/common.html`）で描く。
- BFF の監査ログ: `policy.suspend`（加入者の停止）/ `policy.resume`（加入者の再開）。provisioning-api と provisioner の監査ログにも同じ名前で残り、トレースID で突き合わせられる（provisioner は、応答の状態が読めない場合に `policy.status.update` で残す）。
- 効くのは次の認証からで、接続中のセッションは切れない（確認ダイアログと完了の文で伝える）。
