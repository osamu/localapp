# localapp 設計書

ローカル開発用の DNS + リバースプロキシ常駐デーモン。各アプリに `https://<app>.localapp` 形式の固定 URL を与える。

> 本書は設計と考慮事項を記す。実装状況・開発手順は [CLAUDE.md](CLAUDE.md)、残タスクは [TODO.md](TODO.md)、利用者向け情報は [README.md](README.md) を参照。

## 課題

複数アプリをローカルで並行開発する際、アプリとポート番号の対応を維持するコストが高い。

- ポート番号は起動ごとに変わりうる（衝突時に dev サーバーが自動で変更する）
- `localhost:PORT` はアプリを識別する情報を持たない

本システムは固定 URL による名前付けでこれを解消する。

```
https://app1.localapp        → App1 フロントエンド
https://app1.localapp/api/*  → App1 バックエンド
https://app2.localapp        → App2
https://localapp             → 全アプリ一覧（ダッシュボード）
```

あわせて CLI と Coding Agent 向け SKILL を提供し、エージェントが dev サーバー起動時に自サービスを登録するフローを定義することで、登録漏れを防ぐ。

## 設計原則

1. **ミニマムなコア** — 機能は名前解決・転送・登録簿の 3 点。ダッシュボード・scan は付加機能であり、コアはそれらなしで完結する。
2. **全操作がスクリプタブル** — CLI の全出力は `--json` で機械可読。exit code は意味を持つ。装飾はデフォルト出力に限定する。
3. **CLI は薄いクライアント** — 一次インターフェイスは Unix socket 上の HTTP+JSON API。`curl --unix-socket` で全操作が可能であり、任意の言語から CLI なしで統合できる。
4. **プラットフォーム非依存のコア** — OS 依存処理（resolver 登録・CA 信頼・サービス常駐・特権ポート）は `internal/platform` に隔離する。初版は macOS、次版で Linux。コアのコード変更なしで移植可能であること。
5. **最小の外部依存** — Go 標準ライブラリ + `miekg/dns` のみ。CLI フレームワークは使用しない。単一静的バイナリで配布する。

## アーキテクチャ

単一 Go バイナリ `localapp` が常駐デーモンと CLI を兼ねる。

```
ブラウザ ──(1) app1.localapp を名前解決 ──▶ OS リゾルバ設定 (platform 層)
                                             └─▶ 127.0.0.1:15353 (DNS)
                                                   └─▶ 常に 127.0.0.1 を返す

         ──(2) https://app1.localapp ──────▶ 127.0.0.1:443 (Proxy)
                                             ├─ SNI からオンデマンド証明書発行
                                             └─ registry を参照して転送
                                                   └─▶ localhost:5173

CLI / Agent / curl ─(3) HTTP+JSON ─▶ control.sock ─▶ registry 更新
```

| リスナー | アドレス | 用途 |
|---|---|---|
| DNS (UDP/TCP) | `127.0.0.1:15353` | `*.localapp` → `127.0.0.1` のワイルドカード応答 |
| HTTP | `127.0.0.1:80` | HTTPS へ 301 リダイレクト |
| HTTPS | `127.0.0.1:443` | リバースプロキシ + ダッシュボード |
| Control Plane API | `<state>/control.sock` | HTTP+JSON |

全リスナーは `127.0.0.1` に bind する。`0.0.0.0` での待受は行わない（「セキュリティ」参照）。

## 設計判断

| 論点 | 決定 | 理由 |
|---|---|---|
| 実装言語 | Go | 単一静的バイナリ。DNS/TLS が標準ライブラリ + `miekg/dns` で実装可能。macOS / Linux のクロスコンパイルが容易 |
| DNS | 自作 DNS サーバー | ワイルドカード応答のみで実装が小さい。dnsmasq への依存を排除。アプリ追加時の設定変更が不要 |
| HTTPS | 自作ルート CA + オンデマンド発行 | mkcert 非依存。新規ホスト名は初回アクセス時に発行されるため、アプリ追加時の証明書作業が不要 |
| ルーティング | パス分け / サブドメイン分けの両対応 | パス分けは同一オリジンとなり CORS 設定が不要（推奨既定）。サブドメイン分けは本番構成に近い |
| Control Plane API | HTTP+JSON over Unix socket | ポート衝突なし。ファイルパーミッションによる認可。外部非公開。HTTP のため任意のクライアントで操作可能 |
| ドメイン | `.localapp`（設定可能） | `.local` は mDNS と衝突、`.dev` は実在 TLD かつ HSTS preload 済み。suffix は設定値とする |

## インターフェイス設計

### CLI

`app/service` はパス形式で指定する。

```sh
localapp add 5173                          # app = cwd basename、既定サービス
localapp add 8000 --path /api              # 同 app の api サービスをパスマウント
localapp add 3000 --app app2               # app 明示
localapp ls                                # 一覧
localapp ls --json                         # 一覧（機械可読）
localapp rm app1/api                       # サービス削除
localapp rm app1                           # アプリ削除
localapp open app1                         # ブラウザで開く
```

| コマンド | 用途 |
|---|---|
| `localapp add <port>` | サービス登録（冪等）。`--app --service --path --strip-path --pid --json` |
| `localapp rm <app>[/<service>]` | 登録削除 |
| `localapp ls [--json]` | 一覧（URL・ポート・状態） |
| `localapp open <app>` | ブラウザで開く |
| `localapp status [--json]` | デーモン死活・リスナー・登録数。デーモン停止時は exit 1 |
| `localapp logs [-f]` | デーモンログ |
| `localapp scan` | LISTEN 中の未登録ポートを検出し登録候補として提示 |
| `localapp daemon` | デーモン本体のフォアグラウンド実行（launchd / systemd から起動） |
| `localapp install [--domain <name>]` | resolver 設定・CA 生成 / 信頼登録・サービス常駐登録。sudo を要するのは本コマンドのみ。`--domain` で開発ドメインを指定（既定 `localapp`） |
| `localapp uninstall` | 上記の完全撤去 |
| `localapp ca path` | ルート CA 証明書のパスを出力 |
| `localapp skill show` | SKILL.md を stdout に出力 |
| `localapp skill install <claude\|codex>` | SKILL.md をエージェントのスキルディレクトリへ配置。`--project --dir` |
| `localapp skill uninstall <claude\|codex>` | 配置した SKILL.md を削除。`--project --dir` |
| `localapp version` | バージョンを出力 |

規約:

- exit code: `0` 成功 / `1` エラー / `2` 使い方誤り
- `--json` の出力スキーマは安定インターフェイスとし、破壊的変更をしない
- データは stdout、診断・ログは stderr
- サービス名の既定値: `web`
- アプリ名の既定値: cwd の basename を `[a-z0-9-]` に正規化した値（例: `~/code/my_app` → `my-app`）。決定的に導出されるため、呼び出し元によらず名前が一致する

### Control Plane API

Unix socket 上の HTTP+JSON。CLI は本 API の薄いクライアントである。

```sh
curl --unix-socket /usr/local/var/localapp/control.sock \
     http://localapp/v1/apps

curl --unix-socket /usr/local/var/localapp/control.sock \
     -X PUT http://localapp/v1/apps/app1/services/api \
     -d '{"port": 8000, "path": "/api"}'
```

#### 全体規約

- **トランスポート**: Unix domain socket。導入ユーザー所有・`0600`。パーミッションが認可を兼ねる（認証ヘッダは持たない）
- **URL の authority 部**: 無視する。表記は `http://localapp/...` に統一
- **Content-Type**: リクエスト / レスポンスとも `application/json`
- **バージョニング**: `/v1/` プレフィックス。v1 のフィールドは削除・意味変更しない（追加は可）。クライアントは未知フィールドを無視すること
- **冪等性**: `PUT` / `DELETE` は冪等
- **並行制御**: last-write-wins。デーモン内でシリアライズし、`registry.json` へ atomic write
- **環境変数** `LOCALAPP_SOCKET` でソケットパスを変更可能（テスト・複数インスタンス用）

#### リソースモデル

```
App                              Service
├─ name  [a-z0-9-]{1,63}         ├─ name        [a-z0-9-]{1,63}（既定サービスは "web"）
└─ services: []Service           ├─ port        1-65535（転送先 localhost:port）
                                 ├─ path        パスマウント（省略可。"/" 始まり）
                                 ├─ strip_path  bool（既定 false）
                                 ├─ pid         関連プロセス（省略可。参考情報）
                                 ├─ status      "up" | "down"（サーバー付与。書き込み不可）
                                 └─ urls        []string（サーバー導出。書き込み不可）
```

`status` と `urls` はサーバー導出の読み取り専用フィールド。リクエストボディに含まれても無視する。

#### エンドポイント

| メソッド / パス | 用途 | 成功時 |
|---|---|---|
| `GET /v1/status` | デーモン状態 | 200 |
| `GET /v1/apps` | 全アプリ・サービス一覧 | 200 |
| `GET /v1/apps/{app}` | 単一アプリ | 200 |
| `PUT /v1/apps/{app}/services/{service}` | 登録（冪等 upsert） | 200 |
| `DELETE /v1/apps/{app}/services/{service}` | サービス削除 | 204 |
| `DELETE /v1/apps/{app}` | アプリ削除 | 204 |

**`GET /v1/status`** — レスポンス:

```json
{
  "version": "0.1.0",
  "uptime_sec": 86400,
  "domain": "localapp",
  "listeners": { "dns": "127.0.0.1:15353", "http": "127.0.0.1:80", "https": "127.0.0.1:443" },
  "apps": 3,
  "services": 5
}
```

**`PUT /v1/apps/{app}/services/{service}`** — リクエスト（`port` のみ必須）:

```json
{ "port": 8000, "path": "/api", "strip_path": false, "pid": 12345 }
```

レスポンスは登録結果のサービス全体（導出フィールド込み）:

```json
{
  "app": "app1",
  "service": "api",
  "port": 8000,
  "path": "/api",
  "strip_path": false,
  "status": "up",
  "urls": [
    "https://api.app1.localapp/",
    "https://app1.localapp/api/"
  ]
}
```

**`GET /v1/apps`** — レスポンスは `{"apps": [...]}`。各要素は上記サービス表現を `services` 配列に持つ。

#### エラー

統一形式。`code` は機械可読の安定識別子（追加可、変更・削除不可）:

```json
{ "error": { "code": "invalid_name", "message": "app name must match [a-z0-9-]{1,63}" } }
```

| HTTP | code 例 | 意味 |
|---|---|---|
| 400 | `invalid_name` / `invalid_port` / `invalid_path` / `bad_json` | バリデーション失敗 |
| 404 | `not_found` | 対象の app / service が存在しない（GET / DELETE 時） |
| 405 | `method_not_allowed` | 未定義メソッド |
| 500 | `internal` | 永続化失敗等 |

バリデーションはサーバー側で行う。CLI 側の事前チェックは補助にすぎない。
app / service 名は `[a-z0-9-]{1,63}`、`path` は `/` 始まり、`port` は 1–65535。

#### CLI との対応

| CLI | API |
|---|---|
| `localapp add <port> [--app --service --path --strip-path --pid]` | `PUT /v1/apps/{app}/services/{service}` |
| `localapp rm <app>[/<service>]` | `DELETE /v1/apps/{app}[/services/{service}]` |
| `localapp ls` | `GET /v1/apps` |
| `localapp status` | `GET /v1/status` |

`add --json` / `ls --json` の出力は API レスポンスと同一。CLI 独自の JSON 形式は定義しない。

### 設定

設定ファイルは持たない（初版）。既定値 + 環境変数のみ。

| 環境変数 | 既定値 | 用途 |
|---|---|---|
| `LOCALAPP_DOMAIN` | `localapp` | ドメイン suffix |
| `LOCALAPP_DNS_PORT` | `15353` | DNS リスナーポート |
| `LOCALAPP_HTTP_PORT` | `80` | HTTP リスナーポート（非 root での開発・テスト用オーバーライド） |
| `LOCALAPP_HTTPS_PORT` | `443` | HTTPS リスナーポート（同上） |
| `LOCALAPP_STATE_DIR` | platform 依存 | 状態ディレクトリ |
| `LOCALAPP_SOCKET` | `<state>/control.sock` | 制御ソケット |

#### 開発ドメインの指定

ドメインは `install --domain <name>`（`LOCALAPP_DOMAIN` より優先）で指定する。

- **永続化**: launchd は呼び出し元の環境変数を引き継がないため、install は非既定の設定
  （ドメイン・ポート・状態ディレクトリ）を LaunchDaemon plist の `EnvironmentVariables` へ
  書き込み、常駐デーモンと設定を一致させる（Linux では systemd unit の `Environment=` を同様に用いる）。
  導入ドメインは `<state>/domain` にも記録し、uninstall は環境変数に依存せず削除対象の resolver を特定する
- **検証**: `[a-z0-9-]` ラベルのドット連結のみ許可。`local` / `localhost` とその配下（`*.local` / `*.localhost`）は拒否。
  `.local` は RFC 6762 により全体が mDNS 予約域であり、特に 2 ラベルの apex（例 `dev.local`）は mDNS に取られ
  ダッシュボードへ到達できなくなる
- **変更手順**: CA の Name Constraints はドメインを CA 証明書に焼き込むため、ドメイン変更は
  `uninstall` → `install --domain <new>`（CA 再生成 + 再信頼）が必要。既存 CA と指定ドメインの
  不一致は install が検出してこの手順を案内する
- **推奨値**: 既定の `localapp`、または RFC 6761 予約の `test`。実在 TLD（`dev` 等）は指定しないこと

## ルーティングモデル

登録単位はアプリ配下のサービス。サブドメインルートは登録により常に生成され、パスマウントは `--path` 指定時のみ生成される（非対称ルール。クライアント側の判断分岐を最小化するため）。

```sh
localapp add 5173                    # 既定サービス (web)
localapp add 8000 --service api --path /api
```

生成されるルート:

| URL | 転送先 | 条件 |
|---|---|---|
| `https://app1.localapp/` | `:5173` | 既定サービス |
| `https://app1.localapp/api/*` | `:8000` | `--path` 指定時のみ |
| `https://api.app1.localapp/` | `:8000` | サービス登録により常に生成 |
| `https://localapp/` | ダッシュボード | 固定 |

ホスト解決の優先順:

1. `<service>.<app>.localapp` → 該当サービスへパス透過で転送
2. `<app>.localapp` → パスプレフィックス最長一致
3. 一致なしの場合は既定サービス
4. apex（`localapp`）→ 組み込みダッシュボード

使い分け:

- **パス分け（推奨既定)**: フロントエンドがブラウザから同一アプリの API を呼ぶ構成。同一オリジンとなり CORS 設定・Cookie の SameSite 対応が不要
- **サブドメイン分け**: 本番構成に合わせる場合、または API を独立して呼ぶ場合

`--strip-path`: 既定は strip しない（`/api/users` を `:8000/api/users` へ転送）。バックエンドがルート直下でパスを配信する場合のみ `--strip-path` を指定する。SKILL に判定手順を記載する。

## 登録ライフサイクル

クライアントは dev サーバーの再起動ごとに `add` を呼ぶ。前提となる要件:

- **冪等** — 同一 `app`+`service` への再登録はポート上書きのみ。エラーにしない
- **登録の永続性** — dev サーバー停止時も登録を保持し `down` とマークする。削除は明示的な `rm` のみ。URL の安定性を保証するため
- **死活はプロキシ時に判定** — 転送前に TCP 接続を試行し、失敗時は登録情報（app / service / port）を含むエラーページを返す。ステータスのみの 502 応答は不可
- **`--pid` は任意** — 関連プロセスの参考情報として記録する。死活（`up` / `down`）は常に対象ポートへの TCP dial で判定する
- **永続化** — `registry.json` への atomic write（temp + rename）

## TLS / CA

- **ルート CA**: ECDSA P-256 を初回生成し、OS トラストストアに登録（platform 層）
- **Name Constraints**: CA 証明書自体に X.509 Name Constraints（critical、`permittedDNSDomains: [".localapp"]`）を付与する。秘密鍵が漏洩しても、設定ドメイン外の証明書はブラウザ検証で無効となる
- **リーフ証明書**: `tls.Config.GetCertificate` で SNI ごとにオンデマンド発行。メモリ + ディスクにキャッシュ。有効期間 90 日、自動更新（Apple のローカルルート由来証明書 825 日上限に対するマージン）。`ExtKeyUsageServerAuth` + DNS SAN 必須
- **発行制限**: 設定ドメイン（既定 `.localapp`）配下以外のホスト名への証明書発行は拒否する。Name Constraints と合わせた二重の防御とする
- **SNI 検証**: 発行・キャッシュの前に SNI が `[a-z0-9-]` ラベル + 設定ドメイン suffix に一致することを検証する。不一致はハンドシェイクを拒否する。キャッシュファイル名にホスト名を使用するため、検証はパストラバーサル対策を兼ねる

## プロキシ実装要件

- **WebSocket** — `Upgrade` / `Connection` ヘッダを透過（HMR が使用）
- **SSE / ストリーミング** — `httputil.ReverseProxy` の `FlushInterval: -1`
- **Host ヘッダ** — 原値を保持して転送
- **`X-Forwarded-Proto: https`** — 必須。欠落するとアプリが `http://` の URL を生成する
- **`X-Forwarded-For` / `X-Forwarded-Host`** — 付与
- **レスポンスタイムアウト** — 設定しない。dev サーバーの初回コンパイルは数十秒かかる場合がある

## セキュリティ

### 信頼境界

- 非信頼: ネットワーク上の第三者、ブラウザ上の外部オリジンのページ、マルチユーザーマシンの他ユーザー
- 信頼: 同一マシン・同一ユーザーの他プロセス。`control.sock` のパーミッション（`0600`）がこの境界を実装する

### 脅威と対策

| 領域 | 脅威 | 対策 |
|---|---|---|
| ルート CA 秘密鍵 | 漏洩時、任意ドメインの証明書を発行され MITM に利用される | CA 証明書に Name Constraints（critical、`.localapp` 限定）を付与し、漏洩時も設定ドメイン外の証明書を検証不能にする。鍵は root 所有 `0600`。デーモンの発行制限と二重化（「TLS / CA」参照） |
| リスナー | LAN 上の他端末が proxy 経由で localhost 限定の dev サーバーへ到達する（bind 制限のバイパス） | DNS / 80 / 443 すべて `127.0.0.1` に bind。`0.0.0.0` での待受は行わない |
| SNI | 証明書キャッシュのファイル名にホスト名を使用するため、細工した SNI によるパストラバーサル | 発行・キャッシュ前に SNI を検証し、不一致はハンドシェイク拒否（「TLS / CA」参照） |
| Control Plane API | ブラウザからの CSRF による登録改竄 | 変更系エンドポイントは Unix socket のみに配置し、TCP リスナー（80/443）には公開しない。ダッシュボードは読み取り専用とし、変更操作を持たせない |
| 登録値の出力 | エラーページ・ダッシュボードでの XSS | app / service 名はサーバー側バリデーション（`[a-z0-9-]{1,63}`）済み。出力時も `html/template` による自動エスケープを必須とする |
| root デーモン（macOS） | ネットワーク入力のパース処理を root 権限で実行する | 依存を stdlib + `miekg/dns` に限定し攻撃面を最小化。非 root 化（launchd ソケットアクティベーション）は拡張ポイント。Linux は `CAP_NET_BIND_SERVICE` により非 root で稼働 |
| install / uninstall | root 権限でのファイル書き込み | 書き込み先は固定パスのみとし、ユーザー入力をパスに含めない。uninstall はトラストストア登録を含む全成果物を除去する |
| DNS | DNS rebinding への関与 | 応答は常に `127.0.0.1` / `::1` 固定であり、外部 IP を返さない。設定ドメイン外のクエリは REFUSED |

### アプリ間の隔離

未知 TLD はブラウザの public suffix 処理により第 1 ラベルが suffix として扱われるため、`app1.localapp` と `app2.localapp` は別サイトとなる。`Domain=.localapp` の全アプリ共有 Cookie は設定できず、アプリ間で Cookie は分離される。`api.app1.localapp` と `app1.localapp` は同一サイト（意図された動作）。

### 受容するリスク

- 同一ユーザーで動作する任意のプロセスは Control Plane API 経由で登録を書き換えられる（信頼境界上の設計判断）
- ブラウザから localhost 上の dev サーバーへの到達性は `localhost:PORT` 直接アクセスと同等であり、本システムが新たに拡大するものではない
- Firefox / NSS 系のトラストストアへの CA 登録は手動であり、未登録の間は証明書エラーとなる（「既知の制約と対処」参照）

## プラットフォーム抽象（macOS → Linux）

OS 依存処理は `internal/platform` のインターフェイスに隔離する。コア（registry / proxy / dnsd / ca / control）は platform を import しない。

```go
type Platform interface {
    StateDir() string
    InstallResolver(domain string, dnsPort int) error   // ドメインの DNS 委譲を OS に設定
    UninstallResolver(domain string) error
    InstallTrust(caCertPath string) error               // ルート CA をトラストストアへ登録
    UninstallTrust(caCertPath string) error
    InstallService(execPath string) error               // デーモンの常駐登録
    UninstallService() error
}
```

| 関心事 | macOS（初版） | Linux（次版） |
|---|---|---|
| resolver 登録 | `/etc/resolver/localapp`（`nameserver 127.0.0.1` + `port 15353`。`resolver(5)` の port 対応は man で確認済み） | systemd-resolved の drop-in `/etc/systemd/resolved.conf.d/localapp.conf`（`DNS=127.0.0.1:15353` + `Domains=~localapp`） |
| CA 信頼 | `security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain` | Debian 系: `/usr/local/share/ca-certificates/` + `update-ca-certificates` / RHEL 系: `trust anchor` |
| サービス常駐 | LaunchDaemon（root。macOS は非 root での 80/443 bind 手段を持たない） | systemd unit + `AmbientCapabilities=CAP_NET_BIND_SERVICE`（非 root で稼働可能） |
| 状態ディレクトリ | `/usr/local/var/localapp` | `/var/lib/localapp` |
| 名前解決の適用範囲 | `/etc/resolver` は `getaddrinfo` 経由のみ有効。Go pure resolver / Node `dns.resolve()` には適用されない | resolved stub（`127.0.0.53`）が `/etc/resolv.conf` に載るため pure resolver にも適用される |
| ブラウザの CA | Safari / Chrome はシステムキーチェーンを参照。Firefox は独自ストア（certutil による手動登録） | Chrome / Firefox とも NSS（certutil による手動登録） |

macOS の root 常駐について: launchd ソケットアクティベーション（launchd が root で bind し、ユーザー権限プロセスに FD を渡す）による非 root 化は可能だが、`launch_activate_socket()` が cgo を要求するため初版では採用しない。

### 状態ディレクトリのレイアウト

```
registry.json      — 登録簿（JSON。デーモン停止中は手編集可）
ca/root.crt        — ルート CA 証明書
ca/root.key        — ルート CA 秘密鍵（0600）
certs/<host>.crt   — リーフ証明書キャッシュ
certs/<host>.key
control.sock       — 制御ソケット（導入ユーザー所有・0600）
daemon.log
```

## 既知の制約と対処

| 現象 | 原因 | 対処 |
|---|---|---|
| Vite が `Blocked request` を返す | Vite 5.4+ は未知の Host ヘッダを拒否する | `server.allowedHosts: ['.localapp']` |
| HMR が接続できない | クライアントが `localhost:5173` へ WS 接続を試みる | `server.hmr: { clientPort: 443, protocol: 'wss' }` |
| Next.js dev が警告 / 拒否 | cross-origin dev リクエスト扱いとなる | `next.config` の `allowedDevOrigins` |
| アプリが `http://` の URL を生成する | プロキシ配下の判定材料がない | `X-Forwarded-Proto: https` の付与を確認 |
| Go / Node スクリプトから `.localapp` が解決できない（macOS） | `/etc/resolver` は `getaddrinfo` 経由のみ有効 | ブラウザ・curl は対象外。プログラム間通信は `localhost:PORT` を使用 |
| Docker コンテナから `.localapp` が解決できない | コンテナはホストの resolver 設定を参照しない | `--add-host app1.localapp:host-gateway` + CA をコンテナへ配置 |
| Node / curl が証明書エラー | 独自 CA が未登録 | `NODE_EXTRA_CA_CERTS=$(localapp ca path)` / `SSL_CERT_FILE` |
| Firefox のみ証明書エラー | Firefox は独自トラストストアを持つ | `certutil -A` による手動登録。自動化しない |
| URL バー入力が検索になる | `.localapp` は未知の TLD | `https://` を付けて入力、または `localapp open` |

## Coding Agent 向け SKILL 仕様

`skills/localapp/SKILL.md` に配置する。Agent Skills 形式（frontmatter + 本文の SKILL.md）で記述し、Claude Code / Codex 共通の単一ファイルとする。

### 配布

SKILL.md は `go:embed` でバイナリに埋め込む（単一バイナリ原則）。`localapp skill` サブコマンドで配置する。

| コマンド | 動作 |
|---|---|
| `localapp skill show` | 埋め込み SKILL.md を stdout に出力 |
| `localapp skill install <claude\|codex>` | 下表のディレクトリへ配置（冪等。既存ファイルは上書き） |
| `localapp skill uninstall <claude\|codex>` | 配置したファイルを削除 |

| ターゲット | 既定（ユーザースコープ） | `--project`（cwd 基準） |
|---|---|---|
| `claude` | `~/.claude/skills/localapp/SKILL.md` | `.claude/skills/localapp/SKILL.md` |
| `codex` | `~/.codex/skills/localapp/SKILL.md` | `.codex/skills/localapp/SKILL.md` |

- `--dir <path>` で任意の配置先を指定可能（上記以外のエージェント・レイアウト変更への対応手段）
- SKILL.md の更新はバイナリ更新に追従する。`install` の再実行で最新版に置き換わる
- `install` は sudo 不要（ユーザー領域への書き込みのみ）

### トリガー条件

- dev サーバー起動時 — `npm run dev` / `vite` / `next dev` / `bun dev` / `docker compose up` 等
- ユーザーがプレビュー・起動を指示した時

### 手順

1. `localapp status` を確認する。未導入の場合は sudo を要する操作を実行せず、ユーザーに `localapp install` の実行を促す
2. dev サーバーをバックグラウンド起動し、出力から実際に LISTEN したポートを取得する（指定ポートが使用中の場合、dev サーバーがポートを変更するため）
3. `localapp add <port> --json` で登録する（app 名は cwd から自動決定）
4. ユーザーへの提示 URL はレスポンスの `urls[0]` とする。`localhost:PORT` は提示しない
5. フロントエンド + バックエンド構成の場合、バックエンドを `--service api --path /api` で登録し同一オリジンとする

### 制約

- 判断分岐はパスマウント使用可否の 1 点のみとする。その他は既定値に従う
- SKILL 内にドメイン名を固定しない。開発ドメインは環境ごとに設定される（`install --domain`）ため、
  URL は API レスポンスの `urls` から取得し、ドメインが必要な場合は `status --json` の `domain` を参照する

## 拡張ポイント

初版では実装しない。設計上の排除もしない。

- registry.json の手編集 + 自動リロード（fsnotify）
- `GET /v1/events`（SSE）— 登録変更・死活遷移のイベントストリーム。ダッシュボードのライブ更新や `ls --watch` に使用
- launchd ソケットアクティベーションによる macOS 非 root 化
- プロジェクト設定ファイル（`.localapp.json`）— ルーティングをリポジトリにコミットする用途
- HTTP/3・gRPC 透過
- ダッシュボードは素の HTML に限定し、コアの付加機能以上に拡張しない
