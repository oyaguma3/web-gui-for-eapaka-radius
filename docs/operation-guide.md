# 運用ガイド

- 対象: web-gui-for-eapaka-radius（BFF + Web GUI。コマンド名 `eapaka-webgui`）を導入・運用する人。
- 関連: [README](../README.md)、[設計概要](design-overview.md)、[画面仕様](screen-spec.md)、本PoC（eapaka-radius-server-poc）の `docs/B-02_アプリケーションデプロイ手順書_r*.md` §15（Provisioning API の有効化）と `docs/D-13_Provisioning_API詳細設計書_r*.md`、eapaka-node-provisioner の `docs/operation-guide.md`
- 本書の手順は、2026-10-08 に検証用の実機（Debian 13、Docker Engine と compose プラグイン、Tailscale）で実行して確かめた。11 章（eapaka-node-provisioner 経由）は 2026-10-10 に同じ実機で確かめた。

## 1. 構成

`docker compose` で 2 つのコンテナを動かす。

| コンテナ | 内容 |
|---|---|
| `eapaka-webgui` | BFF と Web GUI。ブラウザ向けの HTTPS を自身で終端し、本PoCの Provisioning API（provisioning-api）を mTLS で呼ぶ |
| `valkey` | BFF 専用の Valkey。アカウント、セッション、BFF の監査ログを保存する。ホストにもほかのプロジェクトにもポートを公開しない（本PoCの Valkey とは別） |

| 待ち受け | コンテナ内のポート | ホスト側の公開先（既定） |
|---|---|---|
| ブラウザ向け HTTPS | 8445 | `127.0.0.1:8445`（`.env` の `EAPAKA_WEBGUI_PUBLISH`） |

| データ | 保存先 |
|---|---|
| アカウント（パスワードは argon2id のハッシュ）、セッション、BFF の監査ログ | ボリューム `valkey-data` |
| 自動生成したブラウザ向けの自己署名証明書と秘密鍵 | ボリューム `webgui-data` |
| Provisioning API 用の証明書、持ち込みのブラウザ向け証明書 | ホストの `certs/`（読み取り専用でマウント） |
| 加入者、RADIUSクライアント、認可ポリシー | 本PoC側（BFF は保存しない） |

GUI はインターネットに直接公開せず、VPN 越しに使う。ホスト OS は Debian を主対象とし、必要なのは Docker Engine と compose プラグインと git だけである。1 つの BFF が扱うのは、本PoCの 1 ノード（provisioning-api 1 つ）である。

接続先は 2 通りから選ぶ（`.env` の `EAPAKA_WEBGUI_ADMIN_API`）。2 章から 9 章は、本PoCの Provisioning API に直接つなぐ場合（既定）の手順である。eapaka-node-provisioner 経由の場合は 11 章を参照。

| 接続先 | 内容 |
|---|---|
| 本PoCの Provisioning API に直接（既定） | BFF と本PoCだけで動く。加入者（鍵）と認可ポリシーは別々に扱う |
| eapaka-node-provisioner（以下「provisioner」）経由 | 鍵を本PoCに置く加入者と aka-only-server に置く加入者を同じ画面で扱い、加入者の登録・削除で認可ポリシーも一緒に扱う。provisioner（専用の Valkey を含む）が別に要る |

本PoCの provisioning-api は 0.3.0 以降（本PoCの main の e2a5a8c 以降）を使う。0.2.0 でも使えるが、セッションの画面と監査ログの「provisioning-api」のタブは「対応していません」と出て使えず、ダッシュボードにセッション数が出ない。

本PoCの Admin TUI とは、原則として同時に使わない。同時に使った場合、他の操作で削除・登録された対象は、画面にその旨を出す。

## 2. 導入

本PoCと同じホストで動かす場合の手順を示す。別のホストの場合は 2.8 を参照。

以下では、2 つのリポジトリを同じディレクトリに並べて置く。本PoCは B-02 の手順で導入済み（`docker compose up -d` で動いている）とする。

```
~/eapaka-radius-server-poc/         本PoC（導入済み）
~/web-gui-for-eapaka-radius/        このリポジトリ
```

同じホストでは、BFF のコンテナは本PoC側が作る共有の Docker ネットワーク（既定の名前は `eapaka-prov`）に参加し、`https://provisioning-api:9444/admin/v1` で provisioning-api に接続する。共有ネットワークには provisioning-api と BFF だけが参加し、BFF から本PoCの Valkey などには届かない。

### 2.1 本PoC側: provisioning-api のサーバー証明書を作る

本PoCの `deployments/` で作業する（B-02 §15.2）。SAN には、BFF が接続に使う名前 `DNS:provisioning-api` を必ず入れる（BFF はホスト名を検証する）。別ホストの BFF からも使う場合は、そのときに接続に使う VPN 側のアドレスも入れておく（2.8）。

```bash
cd ~/eapaka-radius-server-poc/deployments
```

```bash
mkdir -p certs/provisioning
```

```bash
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout certs/provisioning/server.key -out certs/provisioning/server.pem \
  -days 825 -subj "/CN=provisioning-api" \
  -addext "subjectAltName=DNS:provisioning-api,DNS:localhost,IP:127.0.0.1"
```

```bash
chmod 600 certs/provisioning/server.key
```

### 2.2 BFF 側: 取得して `.env` を書く

```bash
cd ~
```

```bash
git clone https://github.com/oyaguma3/web-gui-for-eapaka-radius.git
```

```bash
cd ~/web-gui-for-eapaka-radius
```

```bash
cp .env.example .env
```

```bash
chmod 600 .env
```

少なくとも次の項目を書き換える。全項目は 10 章を参照。

| 項目 | 内容 |
|---|---|
| `EAPAKA_WEBGUI_VALKEY_PASSWORD` | BFF 専用の Valkey のパスワード（本PoCの `VALKEY_PASSWORD` とは別のもの） |
| `EAPAKA_WEBGUI_INITIAL_ADMIN_ID` / `EAPAKA_WEBGUI_INITIAL_ADMIN_PASSWORD` | 最初の管理者（5 章） |
| `EAPAKA_WEBGUI_PUBLISH` | ブラウザ向け HTTPS を公開する VPN 側のアドレスとポート（4 章）。例: `100.64.0.10:8445` |
| `EAPAKA_WEBGUI_TLS_HOSTS` | 自己署名の証明書の SAN。ブラウザで開くときの名前や IP アドレス（3 章）。例: `100.64.0.10` |

イメージをビルドする。

```bash
docker compose build
```

### 2.3 BFF 側: クライアント証明書を作る

provisioning-api に提示するクライアント証明書を作る。証明書と秘密鍵が 1 つの PEM にまとめて標準出力に出る。`-name` の識別名（例 `bff-01`）は、provisioning-api の監査ログの `mgmt_client` になる。

まだ本PoC側の共有ネットワークがない（provisioning-api を起動していない）ので、ここでは共有ネットワークの設定を重ねずに（`COMPOSE_FILE=compose.yaml`）実行する。

```bash
mkdir -p certs
```

```bash
COMPOSE_FILE=compose.yaml docker compose run --rm --no-deps -T eapaka-webgui gen-client-cert -name bff-01 > certs/admin-client.pem
```

標準エラーに、フィンガープリントと、本PoCの `.env` に書く行が出る。

```
クライアント証明書を作りました（CN=bff-01、有効期限 2029-01-10）。
SHA-256 フィンガープリント: b3de2164...d3fdb8
本PoCの .env の PROVISIONING_API_ADMIN_CLIENTS に次の値を登録してください（他の登録があればカンマ区切りで加える）:
PROVISIONING_API_ADMIN_CLIENTS=bff-01=b3de2164...d3fdb8
```

- 有効期間は既定で 825 日（`-days` で変えられる）。期限が切れると provisioning-api に拒否されるので、それまでに作り直して登録し直す。
- 証明書と秘密鍵を別のファイルにしたい場合は、`-out-cert` / `-out-key` で書き出す（既にあるファイルは上書きしない）。その場合は `.env` の `EAPAKA_WEBGUI_ADMIN_CLIENT_KEY` に秘密鍵のコンテナ内のパスを書く。
- 使い方は `COMPOSE_FILE=compose.yaml docker compose run --rm --no-deps -T eapaka-webgui gen-client-cert -h` で表示できる。

### 2.4 本PoC側: BFF を登録して provisioning-api を起動する

本PoCの `.env` に、2.3 で表示された行と、ノード名を書く（B-02 §15.4）。

```
PROVISIONING_API_ADMIN_CLIENTS=bff-01=b3de2164...d3fdb8
PROVISIONING_API_NODE_NAME=<ノード名>
```

provisioning-api を起動する（B-02 §15.5）。このとき共有ネットワーク `eapaka-prov` が作られる。以後、本PoCの `docker compose` の操作には `--profile provisioning` を付ける（B-02 §15.8）。

```bash
cd ~/eapaka-radius-server-poc/deployments
```

```bash
docker compose --profile provisioning up -d --build
```

```bash
docker compose --profile provisioning ps
```

### 2.5 BFF 側: 証明書を置く

provisioning-api のサーバー証明書を、BFF が信頼する証明書として `certs/admin-server.pem` に置く。

```bash
cd ~/web-gui-for-eapaka-radius
```

```bash
cp ../eapaka-radius-server-poc/deployments/certs/provisioning/server.pem certs/admin-server.pem
```

コンテナは UID 65532 で動くので、ファイルの所有者をこの UID にして、パーミッションを 600 にする（所有者がホストのユーザーのままだと、BFF は `permission denied` で起動しない）。

```bash
sudo chown 65532:65532 certs/*.pem
```

```bash
sudo chmod 600 certs/*.pem
```

### 2.6 起動する

本PoC側（provisioning-api）が先に起動している必要がある（共有ネットワークを本PoCの compose が作るため）。

```bash
docker compose up -d
```

provisioning-api に接続できることを確かめる。

```bash
docker compose exec eapaka-webgui /eapaka-webgui check-admin
```

```
接続先: https://provisioning-api:9444/admin/v1
クライアント証明書のフィンガープリント: b3de2164...d3fdb8
接続できました。provisioning-api 0.3.0（ノード simwifi、加入者 0、RADIUSクライアント 0、認可ポリシー 0）
```

`接続できました。` と出れば準備は終わりである。接続できない場合は、原因の見当が表示される（9 章）。

### 2.7 ログインする

ブラウザで `https://<EAPAKA_WEBGUI_PUBLISH のアドレス>:<ポート>/` を開き、最初の管理者でログインする。自己署名の証明書を使っている場合は、ブラウザの警告が出る（3.1）。

最初の管理者で、日常の操作に使う管理者や一般ユーザーのアカウントを作る（5 章）。

### 2.8 別のホストで動かす場合

BFF と本PoCを別のホストで動かす場合は、次のように変える。以下では、本PoCのホストの VPN 側のアドレスを `100.64.0.20` とする。

本PoC側（`deployments/`）:

1. 2.1 のサーバー証明書の SAN に、`IP:100.64.0.20` を加えて作る（既に作ってある場合は作り直し、`docker compose --profile provisioning restart provisioning-api` で読み込ませる）。
2. `.env` に `PROVISIONING_API_BIND=100.64.0.20` を書き、provisioning-api を作り直す（B-02 §15.4）。

   ```bash
   docker compose --profile provisioning up -d
   ```

BFF 側（BFF のディレクトリ）:

1. 2.3 で作った `certs/admin-client.pem` と、本PoCの `server.pem` を `certs/admin-server.pem` として置き、2.5 の手順で所有者を直す。既に置いてあるファイルを置き換える場合は、所有者が UID 65532 なので `sudo cp` で上書きする。
2. `.env` の `COMPOSE_FILE` を `compose.yaml`（共有ネットワークを使わない）にし、`EAPAKA_WEBGUI_ADMIN_URL` を `https://100.64.0.20:9444/admin/v1` にする。
3. 起動して、`check-admin` で確かめる。

   ```bash
   docker compose up -d
   ```

- 別ホストの構成では、provisioning-api のログの `src_ip` は BFF のアドレスではなく、本PoCの compose ネットワークのゲートウェイのアドレスになることがある。provisioning-api 側で BFF を見分けるのは `mgmt_client`（2.3 の識別名）である。

## 3. ブラウザ向けの HTTPS

### 3.1 自己署名の証明書（既定）

初回起動時に、`.env` の `EAPAKA_WEBGUI_TLS_HOSTS` を SAN に入れた自己署名の証明書（有効期間 10 年）を作り、ボリューム `webgui-data` に保存する。

- ブラウザは警告を出す。フィンガープリントを確かめてから進むか、証明書をブラウザ（または OS）の信頼済みの証明書に登録する。
- 起動ログの `loaded server certificate` に、証明書の SHA-256 フィンガープリントが出る。

```bash
docker compose logs eapaka-webgui | grep 'loaded server certificate'
```

`EAPAKA_WEBGUI_TLS_HOSTS` は生成済みの証明書には反映されない。変えた場合は、保存した証明書を消して起動し直す。ボリュームの実際の名前は `<プロジェクト名>_webgui-data` で、プロジェクト名は既定では compose ファイルのあるディレクトリ名になる。

```bash
docker compose stop eapaka-webgui
```

```bash
docker run --rm -v web-gui-for-eapaka-radius_webgui-data:/data valkey/valkey:9 rm /data/tls/cert.pem /data/tls/key.pem
```

最後は `docker compose start` ではなく `docker compose up -d` で起動する（`start` は既存のコンテナをそのまま起動するので、`.env` の変更が反映されず、古い SAN の証明書が作られる）。

```bash
docker compose up -d
```

### 3.2 Tailscale の証明書

tailnet で HTTPS 証明書を有効にしていれば、`tailscale cert` で Let's Encrypt の証明書を取り出せる。ブラウザの警告は出ない。ブラウザでは `https://<マシン名>.<tailnet 名>.ts.net:<ポート>/` で開く。

- tailnet で HTTPS 証明書を有効にすると、マシン名が Certificate Transparency のログに公開される。
- HTTPS 証明書を使うには、tailnet で MagicDNS が有効である必要がある。ホスト OS の DNS 設定と競合する場合は、tailnet の MagicDNS は有効のまま、端末ごとに `sudo tailscale set --accept-dns=false` とし、ブラウザを使う端末の hosts ファイルにマシン名と Tailscale のアドレスを書く。

証明書を `certs/` に取り出す（BFF のディレクトリで実行する。`<マシン名>.<tailnet 名>.ts.net` は `tailscale status --json` の `Self.DNSName`（末尾の `.` を除く）で確かめられる）。

```bash
sudo tailscale cert --cert-file certs/tls-cert.pem --key-file certs/tls-key.pem <マシン名>.<tailnet 名>.ts.net
```

```bash
sudo chown 65532:65532 certs/tls-cert.pem certs/tls-key.pem
```

`.env` で持ち込みの証明書を指定し、起動し直す。

```
EAPAKA_WEBGUI_TLS_CERT=/certs/tls-cert.pem
EAPAKA_WEBGUI_TLS_KEY=/certs/tls-key.pem
```

```bash
docker compose up -d
```

#### 証明書の更新

Let's Encrypt の証明書の有効期間は約 90 日である。`tailscale cert` は実行したときにファイルを書き出すだけなので、定期的に実行する。BFF はファイルが変わったことを 1 分以内に検知して読み直すので、再起動は要らない。

systemd のタイマーで毎日実行する例を示す。`/home/<ユーザー>/web-gui-for-eapaka-radius` は置いた場所に合わせる。

`/etc/systemd/system/eapaka-webgui-cert.service`:

```ini
[Unit]
Description=Renew the Tailscale certificate for eapaka-webgui
After=tailscaled.service

[Service]
Type=oneshot
WorkingDirectory=/home/<ユーザー>/web-gui-for-eapaka-radius
ExecStart=/usr/bin/tailscale cert --cert-file certs/tls-cert.pem --key-file certs/tls-key.pem <マシン名>.<tailnet 名>.ts.net
ExecStartPost=/usr/bin/chown 65532:65532 certs/tls-cert.pem certs/tls-key.pem
```

`/etc/systemd/system/eapaka-webgui-cert.timer`:

```ini
[Unit]
Description=Renew the Tailscale certificate for eapaka-webgui daily

[Timer]
OnCalendar=daily
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl daemon-reload
```

```bash
sudo systemctl enable --now eapaka-webgui-cert.timer
```

一度手で実行して、エラーにならないことを確かめる。

```bash
sudo systemctl start eapaka-webgui-cert.service
```

```bash
systemctl status eapaka-webgui-cert.service --no-pager
```

`tailscale cert` は、有効期限まで十分あるときは同じ証明書を書き出すので、毎日実行しても Let's Encrypt の発行回数の制限には当たらない。

### 3.3 WireGuard などの場合

自己署名の証明書（3.1）を使い、`EAPAKA_WEBGUI_TLS_HOSTS` に VPN 側の IP アドレスを入れておく。ブラウザ側で証明書を信頼する設定をする。

## 4. 公開範囲

Docker が公開したポートは、ufw の規則を通らずに外部から届く。公開範囲は `.env` の `EAPAKA_WEBGUI_PUBLISH` のバインド先アドレスで制御する。

- VPN 側のアドレス（Tailscale なら `tailscale ip -4` の値）を指定する。`0.0.0.0` にはしない。
- バインドできるのは起動時にホストにあるアドレスだけである。VPN のインターフェースが上がる前に Docker が起動すると、コンテナの起動に失敗する（`restart: unless-stopped` により再試行される）。
- 同じホストで aka 版（web-gui-for-aka-only-server。既定 8444）と並べて動かせる。セッションの Cookie の名前も分けてあるので、同じホスト名で両方にログインしても互いに影響しない。

公開先を確かめる。

```bash
docker compose ps --format '{{.Name}} {{.Ports}}'
```

## 5. アカウントの運用

| 種類 | 作り方 | できること |
|---|---|---|
| 最初の管理者 | `.env` の `EAPAKA_WEBGUI_INITIAL_ADMIN_ID` / `EAPAKA_WEBGUI_INITIAL_ADMIN_PASSWORD` | 管理者と一般ユーザーの作成・削除・パスワード再設定、すべての操作 |
| 管理者 | 最初の管理者が作る | 一般ユーザーの作成・削除・パスワード再設定、Ki / OPc の表示と変更、SQN / AMF の変更、RADIUSクライアントの登録・変更・削除と共有シークレットの表示、監査ログ |
| 一般ユーザー | 管理者が作る | 加入者の登録・削除、認可ポリシーの作成・変更・削除、閲覧 |

権限の詳細は[設計概要](design-overview.md)の 4 章を参照。

- ユーザーID は provisioning-api の操作者（`X-Operator-Id`）として、provisioning-api の監査ログの `admin_user` に残る。
- 最初の管理者は Valkey に保存しない。パスワードを変えるときは `.env` を書き換えて `docker compose up -d` で起動し直す。最初の管理者のセッションは無効になる（パスワードを変えずに起動し直した場合はセッションが続く）。
- 最初の管理者は、作業の初期設定と緊急時に使い、日常の操作は個別のアカウントで行う。
- アカウントを作ったり、パスワードを再設定したりすると、そのアカウントは次のログインでパスワードの変更を求められる。
- パスワードは 8 文字以上で、ユーザーID と同じものは使えない。

### 5.1 ログインのロック

同じユーザーID で `EAPAKA_WEBGUI_LOGIN_MAX_FAILURES` 回（既定 5 回）続けて失敗すると、最初の失敗から `EAPAKA_WEBGUI_LOGIN_LOCK_DURATION`（既定 15 分）が過ぎるまで、そのユーザーID ではログインできない。待たずに解除する場合は、BFF の Valkey から失敗回数を消す（`<ユーザーID>` を置き換える）。

```bash
docker compose exec valkey sh -c 'valkey-cli -a "$VALKEY_PASSWORD" --no-auth-warning del loginfail:<ユーザーID>'
```

### 5.2 セッション

- 操作がないまま `EAPAKA_WEBGUI_SESSION_IDLE_TIMEOUT`（既定 30 分）過ぎるか、ログインから `EAPAKA_WEBGUI_SESSION_MAX_AGE`（既定 12 時間）過ぎると、ログアウトする。
- パスワードの変更・再設定とアカウントの削除で、そのアカウントの他のセッションは無効になる。
- BFF を起動し直してもセッションは残る。

## 6. ログと監査ログ

| 種類 | 内容 | 見る場所 |
|---|---|---|
| BFF のログ | アクセスログ（トレースID つき）、Provisioning API の呼び出し、エラー | `docker compose logs eapaka-webgui`（標準出力の JSON） |
| BFF の監査ログ | ログインの成功・失敗、アカウントの作成・削除、パスワードの再設定・変更、BFF を通した Provisioning API の操作（加入者・RADIUSクライアント・認可ポリシーの変更、Ki / OPc と共有シークレットの表示） | 画面の「監査ログ」の「BFF」のタブ（管理者）。標準出力にも出る |
| provisioning-api の監査ログ | Provisioning API を通した変更操作と秘密の値の取得（操作者 `admin_user`、管理クライアント `mgmt_client`）。BFF 以外の管理クライアントの操作も含む。Admin TUI の操作は含まない | 画面の「監査ログ」の「provisioning-api」のタブ（管理者。本PoC側の `PROVISIONING_API_AUDIT_MAX` 件、既定 10000 件まで）。すべては本PoCのホストの `deployments/logs_on_host/provisioning-api.log` |
| provisioning-api のログ | Provisioning API の呼び出し、エラー | 本PoCのホストの `deployments/logs_on_host/provisioning-api.log` |

```bash
docker compose logs -f eapaka-webgui
```

BFF は Provisioning API を呼ぶとき、操作ごとのトレースID を `X-Trace-ID` で渡す。BFF の監査ログの内容の `trace_id` は、「provisioning-api」のタブのトレースID と同じになる。provisioning-api のログファイルでその操作の前後のログを探す場合は、本PoCの `deployments/` で次を実行する（`<trace_id>` を置き換える）。

```bash
grep <trace_id> logs_on_host/provisioning-api.log
```

- BFF の監査ログの「操作元」とアクセスログの `remote` は、Docker のポート公開を経由するため、ブラウザのアドレスではなく BFF の compose ネットワークのゲートウェイのアドレス（例 `172.20.0.1`）になることがある（2026-10-08 に検証機で確認）。操作者はユーザーID で見分ける。
- BFF の監査ログは `EAPAKA_WEBGUI_AUDIT_MAX` 件（既定 10000）を超えると古いものから消える。長く残したい場合は、Docker のログを外部に保存する。
- パスワード、Ki、OPc、共有シークレットは BFF のログにも監査ログにも出ない。
- Docker のログは既定では無制限に増える。`/etc/docker/daemon.json` などでローテーションを設定しておく。

## 7. バックアップと復元

BFF のデータは 2 つのボリュームにある。加入者などのデータは本PoC側にあるので、本PoCのバックアップも別に取る（B-02 §9）。

| ボリューム | 内容 | 失った場合 |
|---|---|---|
| `<プロジェクト名>_valkey-data` | アカウント、セッション、BFF の監査ログ | 最初の管理者以外のアカウントを作り直す |
| `<プロジェクト名>_webgui-data` | 自己署名の証明書と秘密鍵 | 起動時に作り直される（ブラウザの信頼設定をやり直す） |

以下はプロジェクト名が `web-gui-for-eapaka-radius` の場合の例である。

### 7.1 バックアップ

```bash
docker compose stop
```

```bash
docker run --rm -v web-gui-for-eapaka-radius_valkey-data:/data:ro -v "$PWD":/backup valkey/valkey:9 tar czf /backup/webgui-valkey-data.tgz -C /data .
```

```bash
docker compose start
```

バックアップのファイルは、コンテナの中で作るので root の所有（パーミッション 600）になる。移すときは `sudo` を使う。

### 7.2 復元

ボリュームを空にしてから、バックアップを展開する。現在のアカウントは消え、バックアップの時点のアカウントとセッションに戻る。

```bash
docker compose down
```

```bash
docker run --rm -v web-gui-for-eapaka-radius_valkey-data:/data -v "$PWD":/backup valkey/valkey:9 sh -c 'rm -rf /data/* && tar xzf /backup/webgui-valkey-data.tgz -C /data'
```

```bash
docker compose up -d
```

## 8. 更新

更新の前にバックアップを取っておく（7 章）。データはボリュームに残る。

```bash
git pull
```

設定項目が増えていないかを確かめる。`git pull` は更新前の位置を `ORIG_HEAD` に残すので、`.env.example` の変更を表示できる。何も表示されなければ、そのまま進む。

```bash
git diff ORIG_HEAD HEAD -- .env.example
```

増えた項目があれば、`.env` に書き足す。

ビルドし直して起動する。

```bash
docker compose up -d --build
```

```bash
docker compose exec eapaka-webgui /eapaka-webgui check-admin
```

画面の下に出るバージョンは、`.env` の `EAPAKA_WEBGUI_VERSION`（既定 `dev`）で決まる。git のコミットを出したい場合は、ビルドのときに指定する。

```bash
EAPAKA_WEBGUI_VERSION=$(git rev-parse --short HEAD) docker compose up -d --build
```

本PoCの Provisioning API が更新された場合は、本PoC側の更新（B-02 §12）も行い、`check-admin` で接続を確かめる。

## 9. 障害時の確認

まず `check-admin` で provisioning-api への接続を確かめる。

```bash
docker compose exec eapaka-webgui /eapaka-webgui check-admin
```

| 症状・表示 | 確認すること |
|---|---|
| BFF が起動しない | `docker compose logs eapaka-webgui` の `error:`。最初の管理者の設定（未設定、パスワードが 8 文字未満、ユーザーID の形式）、Valkey のパスワード、`certs/` のファイルの有無と所有者（UID 65532。`permission denied` なら 2.5） |
| `network eapaka-prov declared as external, but could not be found` | 本PoCを `docker compose --profile provisioning up -d` で起動しているか。本PoC側の `PROVISIONING_SHARED_NETWORK` と `.env` の `PROVISIONING_SHARED_NETWORK` が一致しているか。別ホストなら `COMPOSE_FILE=compose.yaml` にする。クライアント証明書を作るとき（2.3）は `COMPOSE_FILE=compose.yaml` を付ける |
| 「provisioning-api が BFF のクライアント証明書を受け付けませんでした」「provisioning-api が接続を切りました」 | 本PoC側の `PROVISIONING_API_ADMIN_CLIENTS` に、`check-admin` が表示するフィンガープリントが登録されているか。登録した後に provisioning-api を作り直したか（`docker compose --profile provisioning up -d provisioning-api`）。クライアント証明書が有効期間内か。provisioning-api のログの `PROV_CLIENT_REJECTED`（B-02 §15.7）。「接続を切りました」で `PROV_CLIENT_REJECTED` が出ていなければ、provisioning-api の再起動中などに接続が切れただけのこともある（もう一度試す） |
| 「サーバー証明書が、設定した証明書と一致しません」 | `certs/admin-server.pem` が、本PoCの `deployments/certs/provisioning/server.pem` と同じか（作り直した後は置き直す） |
| 「サーバー証明書に、接続先のホスト名（または IP アドレス）が入っていません」 | サーバー証明書の SAN に、`EAPAKA_WEBGUI_ADMIN_URL` のホスト名（同一ホストなら `DNS:provisioning-api`）か IP アドレスが入っているか（`openssl x509 -in certs/provisioning/server.pem -noout -ext subjectAltName`） |
| 「ホスト名（provisioning-api）を解決できません」 | 本PoCの provisioning-api が起動しているか（`--profile provisioning`）。BFF が共有ネットワークに参加しているか（`COMPOSE_FILE`） |
| 「時間内に応答がありません」 | 接続先のアドレス、VPN、本PoC側の公開先（`PROVISIONING_API_BIND`） |
| ログインできない（「一時的にログインできません」） | ログインのロック（5.1） |
| 画面が開けない | `EAPAKA_WEBGUI_PUBLISH` のアドレスで待ち受けているか（4 章）、VPN がつながっているか |
| 「他の操作（本PoCの Admin TUI など）で削除された可能性があります」 | 本PoCの Admin TUI などで同じ対象を変更していないか。一覧を開き直す |
| 本PoCの `docker compose --profile provisioning down` で `Network eapaka-prov Resource is still in use` と出る | BFF が共有ネットワークに参加しているため、ネットワークが残っただけで、問題はない。本PoCを起動し直せば、BFF は再起動なしで接続し直す |

## 10. 環境変数

`.env` に書く。compose は `.env` の値をコンテナの環境変数として渡す。

| 変数 | 既定値 | 内容 |
|---|---|---|
| `COMPOSE_FILE` | `compose.yaml:compose.eapaka-prov.yaml` | 別ホストなら `compose.yaml`。同一ホストの provisioner 経由なら `compose.yaml:compose.eapaka-provisioner.yaml`（11 章） |
| `PROVISIONING_SHARED_NETWORK` | `eapaka-prov` | 同一ホストの本PoCが作る共有ネットワークの名前（本PoC側と同じにする） |
| `PROVISIONER_SHARED_NETWORK` | `eapaka-provisioner` | 同一ホストの provisioner が作る共有ネットワークの名前（provisioner 側と同じにする） |
| `EAPAKA_WEBGUI_ADMIN_API` | `provisioning-api` | 接続先の種類（`provisioning-api` / `provisioner`。11 章） |
| `EAPAKA_WEBGUI_ADMIN_URL` | （空: 接続先の種類ごとの同一ホストの URL） | 接続先のベース URL。空なら `https://provisioning-api:9444/admin/v1`（provisioner 経由なら `https://eapaka-provisioner:9446/admin/v1`） |
| `EAPAKA_WEBGUI_ADMIN_CLIENT_KEY` | （空: クライアント証明書のファイルから読む） | クライアント証明書の秘密鍵を別のファイルにした場合の、コンテナ内のパス（`/certs/...`） |
| `EAPAKA_WEBGUI_VALKEY_PASSWORD` | （必須） | BFF 専用の Valkey のパスワード |
| `EAPAKA_WEBGUI_INITIAL_ADMIN_ID` | （必須） | 最初の管理者のユーザーID（英数字と `.` `_` `@` `-` の 64 文字まで） |
| `EAPAKA_WEBGUI_INITIAL_ADMIN_PASSWORD` | （必須） | 最初の管理者のパスワード（8 文字以上） |
| `EAPAKA_WEBGUI_PUBLISH` | `127.0.0.1:8445` | ブラウザ向け HTTPS を公開するホスト側のアドレスとポート |
| `EAPAKA_WEBGUI_TLS_HOSTS` | `localhost,127.0.0.1` | 自己署名の証明書の SAN |
| `EAPAKA_WEBGUI_TLS_CERT` / `EAPAKA_WEBGUI_TLS_KEY` | （空: 自己署名） | 持ち込みの証明書と秘密鍵のコンテナ内のパス（`/certs/...`） |
| `EAPAKA_WEBGUI_SESSION_IDLE_TIMEOUT` | `30m` | 無操作でログアウトするまでの時間 |
| `EAPAKA_WEBGUI_SESSION_MAX_AGE` | `12h` | ログインからログアウトするまでの最大時間 |
| `EAPAKA_WEBGUI_LOGIN_MAX_FAILURES` | `5` | ログインをロックする失敗回数 |
| `EAPAKA_WEBGUI_LOGIN_LOCK_DURATION` | `15m` | ロックする時間（最初の失敗から） |
| `EAPAKA_WEBGUI_AUDIT_MAX` | `10000` | BFF の監査ログの保持件数 |
| `TZ` | `Asia/Tokyo` | 画面とログの日時のタイムゾーン |
| `EAPAKA_WEBGUI_LOG_LEVEL` | `info` | ログのレベル（debug / info / warn / error） |
| `EAPAKA_WEBGUI_VERSION` | `dev` | 画面に出すバージョン（ビルド時に埋め込む） |

コンテナの中では、このほかに `EAPAKA_WEBGUI_ADDR`（`:8445`）、`EAPAKA_WEBGUI_VALKEY_ADDR`（`valkey:6379`）、`EAPAKA_WEBGUI_ADMIN_CLIENT_CERT`（`/certs/admin-client.pem`）、`EAPAKA_WEBGUI_ADMIN_SERVER_CERT`（`/certs/admin-server.pem`）を使う。compose を使わずに動かす場合は、これらも指定する。

## 11. eapaka-node-provisioner 経由でつなぐ

`.env` の `EAPAKA_WEBGUI_ADMIN_API=provisioner` にすると、BFF は本PoCの Provisioning API ではなく provisioner に接続する（設計概要 §12）。provisioner は、本PoCの Provisioning API と aka-only-server の管理API を組み合わせて加入者を扱う統合API で、RADIUSクライアント・認可ポリシー・セッションは Provisioning API と同じ形で中継する。

```
[ブラウザ] ──HTTPS──> BFF ──mTLS──> provisioner ──mTLS──> provisioning-api（本PoC）
                                         │        └─mTLS──> aka-only-server の管理API
                                   Valkey（provisioner 専用）
```

- BFF の管理クライアントの登録先は provisioner（provisioner の `.env` の `PROVISIONER_ADMIN_CLIENTS`）になり、BFF が信頼するサーバー証明書は provisioner のもの（provisioner の `server-cert` の出力）になる。本PoCの `PROVISIONING_API_ADMIN_CLIENTS` への BFF の登録は要らない（provisioner が自分の証明書で本PoCに接続する）。
- アカウント・権限・BFF の監査ログはそのまま使える（BFF の Valkey は接続先によらない）。
- 1 回の呼び出しの上限は 60 秒（直接のときは 10 秒）。provisioner の加入者の登録は、補償を含めて下流を何度か呼ぶため。

### 11.1 前提

- provisioner を、provisioner の運用ガイド 2 章（同一ホスト）または 3 章（別ホスト）の手順で導入し、`check-downstream` で本PoCと aka-only-server に接続できることを確かめておく。provisioner の `PROVISIONER_ADMIN_CLIENTS` には、11.2 で BFF の証明書を登録する（provisioner は管理クライアントが 1 つもないと起動しないので、先に登録してから起動してもよい）。
- 同じホストでは、BFF は provisioner が作る共有ネットワーク（既定の名前は `eapaka-provisioner`）に参加し、`https://eapaka-provisioner:9446/admin/v1` で接続する。そのための設定が `compose.eapaka-provisioner.yaml` である。provisioner を先に起動する。

### 11.2 新しく導入する場合（同一ホスト）

以下では、BFF と provisioner のリポジトリを同じディレクトリに並べて置く（`~/web-gui-for-eapaka-radius/`、`~/eapaka-node-provisioner/`）。

1. 2.2 のとおり取得して `.env` を作り、次の 2 行を書き換える。`EAPAKA_WEBGUI_ADMIN_URL` は空のままにする（provisioner の同一ホストの URL になる）。

   ```
   COMPOSE_FILE=compose.yaml:compose.eapaka-provisioner.yaml
   EAPAKA_WEBGUI_ADMIN_API=provisioner
   ```

2. 2.3 のとおりクライアント証明書を作る（`COMPOSE_FILE=compose.yaml` を付けて実行する）。標準エラーに出る行のうち、`PROVISIONER_ADMIN_CLIENTS=` の行を使う。

   ```
   接続先の .env に次の値を登録してください（他の登録があればカンマ区切りで加える）:
     本PoCの Provisioning API に直接つなぐ場合（本PoCの .env）:
   PROVISIONING_API_ADMIN_CLIENTS=bff-01=e7cb4ad2...68ab435
     eapaka-node-provisioner 経由でつなぐ場合（provisioner の .env）:
   PROVISIONER_ADMIN_CLIENTS=bff-01=e7cb4ad2...68ab435
   ```

3. provisioner 側: `.env` の `PROVISIONER_ADMIN_CLIENTS` にその値を書き（ほかの管理クライアントがあればカンマで区切って加える）、provisioner を作り直す。続けて、provisioner のサーバー証明書を BFF の `certs/admin-server.pem` に取り出す。

   ```bash
   cd ~/eapaka-node-provisioner
   ```

   ```bash
   docker compose up -d
   ```

   ```bash
   docker compose exec -T eapaka-provisioner /eapaka-provisioner server-cert > ../web-gui-for-eapaka-radius/certs/admin-server.pem
   ```

4. BFF 側: 2.5 のとおり `certs/` の所有者を直し、起動して確かめる。

   ```bash
   cd ~/web-gui-for-eapaka-radius
   ```

   ```bash
   sudo chown 65532:65532 certs/*.pem
   ```

   ```bash
   sudo chmod 600 certs/*.pem
   ```

   ```bash
   docker compose up -d
   ```

   ```bash
   docker compose exec eapaka-webgui /eapaka-webgui check-admin
   ```

   ```
   接続先: https://eapaka-provisioner:9446/admin/v1（provisioner）
   クライアント証明書のフィンガープリント: e7cb4ad2...68ab435
   接続できました。provisioner dev
     本PoCの Provisioning API: 0.3.0（ノード simwifi、加入者 0）
     aka-only-server: dev（加入者 0）
     vector-gateway の AVクライアント: ID 1（vector-gateway）
     PLMN マップ: 44020=aka
   ```

   provisioner に接続できない、または接続先が provisioner でない（`EAPAKA_WEBGUI_ADMIN_URL` が provisioning-api を指している等）場合は、原因の見当を出してエラーで終わる。provisioner の先の下流（本PoC、aka-only-server）に接続できない場合は、その旨と provisioner が返す原因の見当を表示する（このときはエラーにしない。provisioner 側の `check-downstream` で確かめる）。

### 11.3 直接接続から切り替える

直接接続で動いている BFF を provisioner 経由にする場合は、次を行う。BFF のクライアント証明書（`certs/admin-client.pem`）とアカウントはそのまま使える。

1. provisioner 側: 11.2 の 3 のとおり、BFF のフィンガープリント（`check-admin` が表示する値）を `PROVISIONER_ADMIN_CLIENTS` に登録して作り直し、サーバー証明書を取り出す。BFF の `certs/admin-server.pem` は所有者が UID 65532 なので、`sudo cp` で上書きして所有者を直す。

   ```bash
   cd ~/eapaka-node-provisioner
   ```

   ```bash
   docker compose exec -T eapaka-provisioner /eapaka-provisioner server-cert > ~/provisioner-server.pem
   ```

   ```bash
   cd ~/web-gui-for-eapaka-radius
   ```

   ```bash
   sudo cp ~/provisioner-server.pem certs/admin-server.pem
   ```

   ```bash
   sudo chown 65532:65532 certs/admin-server.pem
   ```

   ```bash
   sudo chmod 600 certs/admin-server.pem
   ```

2. BFF の `.env` を次のようにする。`EAPAKA_WEBGUI_ADMIN_URL` に provisioning-api の URL が書いてあれば、空にする（または provisioner の URL にする）。

   ```
   COMPOSE_FILE=compose.yaml:compose.eapaka-provisioner.yaml
   EAPAKA_WEBGUI_ADMIN_API=provisioner
   EAPAKA_WEBGUI_ADMIN_URL=
   ```

3. 作り直して確かめる（11.2 の 4 と同じ表示になる）。BFF のコンテナは本PoCの共有ネットワークから外れ、provisioner の共有ネットワークだけに参加する。

   ```bash
   docker compose up -d
   ```

   ```bash
   docker compose exec eapaka-webgui /eapaka-webgui check-admin
   ```

- 切り替えた後は、本PoCの `.env` の `PROVISIONING_API_ADMIN_CLIENTS` から BFF の登録を外してよい（provisioner の登録は残す）。直接接続に戻すときは、もう一度登録する（11.5）。
- ログイン中のセッションは、作り直しても続く。

### 11.4 別ホストの provisioner につなぐ

provisioner が別のホストにある場合は、provisioner 側で API を VPN 側のアドレスで公開し、サーバー証明書の SAN にそのアドレスを入れる（provisioner の運用ガイド 3.4）。以下では、provisioner のホストの VPN 側のアドレスを `100.64.0.30` とする。

1. provisioner 側の 3.4 を行い、作り直したサーバー証明書を取り出して、BFF の `certs/admin-server.pem` に置く（11.3 の 1 と同じく `sudo cp` と所有者の変更）。
2. BFF の `.env` を次のようにして、作り直す。

   ```
   COMPOSE_FILE=compose.yaml
   EAPAKA_WEBGUI_ADMIN_API=provisioner
   EAPAKA_WEBGUI_ADMIN_URL=https://100.64.0.30:9446/admin/v1
   ```

   ```bash
   docker compose up -d
   ```

3. `check-admin` で確かめる（接続先が `https://100.64.0.30:9446/admin/v1（provisioner）` になる）。

### 11.5 直接接続に戻す

1. 本PoCの `.env` の `PROVISIONING_API_ADMIN_CLIENTS` に BFF のフィンガープリントがなければ登録し、provisioning-api を作り直す（2.4）。
2. BFF の `certs/admin-server.pem` を本PoCのサーバー証明書（`deployments/certs/provisioning/server.pem`）に戻す（`sudo cp` と所有者の変更）。
3. BFF の `.env` を直接接続の値に戻し（同一ホストなら次のとおり）、作り直して `check-admin` で確かめる。

   ```
   COMPOSE_FILE=compose.yaml:compose.eapaka-prov.yaml
   EAPAKA_WEBGUI_ADMIN_API=provisioning-api
   EAPAKA_WEBGUI_ADMIN_URL=
   ```

   ```bash
   docker compose up -d
   ```

画面は直接接続のものに戻る（メニューの「操作の記録」はなくなる）。provisioner の操作の記録は provisioner に残る。

### 11.6 画面と運用の違い

画面の詳細は画面仕様の 11 章を参照。

- **加入者**: 鍵の置き場所（本PoC / aka-only-server）は、IMSI の PLMN と provisioner の PLMN マップで決まる（選べない）。登録では認可ポリシーの既定の動作も選び（ルールは登録後に認可ポリシーの画面で編集）、削除では認可ポリシーも消える。2 つのノードの食い違い（鍵がない、認可ポリシーがない等）は、一覧と詳細に出る。
- **操作の記録**: 加入者の登録・変更・削除が途中で失敗して元に戻せなかった操作は、provisioner が後で自動でやり直す。「操作の記録」の画面で状況を確かめられ、管理者が「やり直す」「閉じる」を行える（判断の仕方は provisioner の運用ガイド 6 章）。完了していない操作がある IMSI は、加入者の登録・変更・削除と認可ポリシーの保存・削除ができない（その操作へのリンクつきで断られる）。
- **監査ログ**: 「provisioner」「aka-only-server」のタブが加わる（provisioner が aka-only-server を扱わない設定なら「aka-only-server」は出ない）。BFF の操作の `trace_id` は、provisioner と下流の監査ログのトレースID と同じになる。provisioning-api と aka-only-server の監査ログの管理クライアントは provisioner の識別名になる。
- **ほかの操作手段との併用**: provisioner の同じ IMSI の排他は、provisioner を通る操作どうしでだけ効く。provisioner 経由で使う間は、同じ加入者を本PoCの Admin TUI や aka-only-server の管理 GUI で操作しない（provisioner の運用ガイド 1.2）。
- **バックアップ**: provisioner 経由でも、BFF のバックアップは 7 章のとおり。provisioner と下流のバックアップは、それぞれの手順で取る。

### 11.7 障害時の確認

まず `check-admin` で provisioner への接続と、provisioner から下流への接続を確かめる。9 章の表のうち、provisioning-api を provisioner に読み替えたものに加え、次を確かめる。

| 症状・表示 | 確認すること |
|---|---|
| `network eapaka-provisioner declared as external, but could not be found` | provisioner を先に起動しているか。provisioner 側の `PROVISIONER_SHARED_NETWORK` と BFF の `.env` の `PROVISIONER_SHARED_NETWORK` が一致しているか。別ホストなら `COMPOSE_FILE=compose.yaml` にする。クライアント証明書を作るとき（2.3）は `COMPOSE_FILE=compose.yaml` を付ける |
| 「provisioner が BFF のクライアント証明書を受け付けませんでした」 | provisioner の `.env` の `PROVISIONER_ADMIN_CLIENTS` に、`check-admin` が表示するフィンガープリントが登録されているか。登録した後に provisioner を作り直したか（`docker compose up -d`）。provisioner のログの `admin client certificate rejected` |
| 「provisioner のサーバー証明書が、設定した証明書（EAPAKA_WEBGUI_ADMIN_SERVER_CERT）と一致しません」 | `certs/admin-server.pem` が provisioner の `server-cert` の出力と同じか（provisioner のサーバー証明書を作り直した後は置き直す） |
| 「接続先は provisioner ではないようです」「接続先は provisioner のようです」 | `EAPAKA_WEBGUI_ADMIN_API` と `EAPAKA_WEBGUI_ADMIN_URL` の組み合わせ（直接接続の URL が残っていないか） |
| ダッシュボードで「provisioner から接続できません」（本PoC / aka-only-server） | provisioner の先の下流の問題。表示される原因の見当と、provisioner の `check-downstream`（provisioner の運用ガイド 10 章） |
| 「provisioner から aka-only-server に接続できません。」（加入者の登録などで 503） | 同上。書き込みを始める前に断っているので、何も変わっていない |
| 「同じ IMSI に完了していない操作が残っています」 | 表示されるリンクから操作の記録を開き、やり直すか、下流を直して閉じる（11.6） |
| 「… への操作が途中で失敗し、元に戻せませんでした」 | provisioner が自動でやり直す。操作の記録で状況を確かめる。下流が直らないまま 24 時間たつと「失敗」になり、手での対応が要る |
| 「同じ IMSI の操作が処理中です」 | ほかの操作（別の画面、別の管理クライアント）が同じ IMSI を処理している。少し待ってからやり直す |

