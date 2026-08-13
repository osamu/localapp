# localapp

ローカル開発用の DNS + リバースプロキシ常駐デーモン。dev サーバーに `https://<アプリ名>.<ドメイン>` 形式の固定 URL を与える。

```
https://myapp.localapp        → localhost:5173 (フロントエンド)
https://myapp.localapp/api/*  → localhost:8000 (バックエンド、同一オリジン)
https://localapp              → 登録アプリ一覧ダッシュボード
```

- ポート番号が起動ごとに変わっても URL は変わらない
- HTTPS（ローカル CA による正規の証明書。ブラウザに警告は出ない）
- フロントとバックエンドを同一オリジンに載せられるため CORS / Cookie 設定が不要
- 対応 OS: macOS（Linux 対応は計画中）

## インストール

```sh
curl -fsSL https://raw.githubusercontent.com/osamu/localapp/main/install.sh | sh
```

スクリプトは最新リリースのバイナリをダウンロードし、sha256 チェックサムを検証して
`/usr/local/bin` へ配置する（`LOCALAPP_BIN_DIR` で配置先、`LOCALAPP_VERSION` で
バージョンを指定可能）。内容を確認してから実行したい場合は [install.sh](install.sh)
（約 70 行）を読むか、ソースからビルドする:

```sh
go build -o /usr/local/bin/localapp ./cmd/localapp
```

続けて初回セットアップを実行する（DNS resolver とトラストストアに触れるため、
スクリプトからは意図的に実行しない）:

```sh
sudo localapp install                       # 既定ドメイン localapp
sudo localapp install --domain dev.test     # ドメインを指定する場合
```

`install` が行うこと（sudo が必要なのはここだけ）:

| 対象 | 内容 |
|---|---|
| `/etc/resolver/<ドメイン>` | 対象ドメインの名前解決をローカル DNS へ委譲（他のドメインの DNS 設定には影響しない） |
| システムキーチェーン | ローカル CA を信頼登録（対象ドメイン以外には発行できない制約付き） |
| `/Library/LaunchDaemons/` | デーモンの常駐登録 |

ドメインには `local` / `localhost` とその配下は指定できない（mDNS 等の予約域）。
変更する場合は `sudo localapp uninstall` 後に `--domain` を付けて再インストールする。

## 仕組み

1 バイナリ・1 デーモン・3 つのループバックリスナー（DNS・プロキシ・制御ソケット）。

```
          ┌─────────────────────── your machine ────────────────────────┐
          │                                                             │
 browser ─┤ ① "myapp.localapp?" ▶ /etc/resolver/localapp               │
          │                        └▶ 127.0.0.1:15353 (DNS)             │
          │                            always answers: 127.0.0.1        │
          │                                                             │
          ├ ② https://myapp.localapp ▶ 127.0.0.1:443 (proxy)           │
          │      ▲                       ├ ③ mints a cert for the SNI  │
          │      │ cert chains to        │    on the fly (local CA)     │
          │      │ your local CA         └ looks up "myapp" ▶ :5173     │
          │                                                             │
 CLI /    ├ ④ HTTP+JSON over a unix socket ▶ registry { app → port }   │
 agent    │                                                             │
          └─────────────────────────────────────────────────────────────┘
```

1. **名前解決** — `install` は `/etc/resolver/<ドメイン>` という 1 ファイルを置くだけ。
   macOS は `*.<ドメイン>` の DNS クエリ（それ以外は対象外）をローカル DNS サーバーへ送り、
   この DNS はどんな名前にも `127.0.0.1` を返す。アプリを追加しても DNS には二度と触らない。

2. **ルーティング** — ブラウザは `127.0.0.1:443` のプロキシへ接続する。プロキシはホスト名から
   registry を引き、`localhost:<port>` へ転送する。WebSocket（HMR）・ストリーミング・`Host`
   ヘッダは透過。パスマウント（`/api/*`）は同一オリジンになるため CORS は登場しない。

3. **HTTPS** — TLS ハンドシェイク中に、そのホスト名の証明書をその場で発行する（90 日キャッシュ）。
   署名するのは install 時に 1 回だけ作られるローカル CA で、critical な **Name Constraints**
   により開発ドメイン外は暗号学的に保証不能。

4. **登録** — `localapp add 5173` や `localapp run`、あるいは同梱 SKILL 経由の AI エージェントが、
   Unix socket 上の HTTP API で registry に `{app → port}` を書き込む。サーバーが止まっても `down` 表示になる
   だけで、URL 自体は永続する。

`/etc/hosts` の編集も、アプリごとの設定もない。ループバック外で listen するものは何もない。

## 使い方

```sh
localapp run -- npm run dev       # 空きポートを PORT として注入し、登録して実行
localapp add 5173                 # 既に動いているサーバーに後付け登録
localapp ls                       # 一覧（URL・ポート・状態）
localapp open myapp               # ブラウザで開く
localapp rm myapp                 # 登録削除
localapp scan                     # 未登録の LISTEN ポートを検出
```

フロントエンド + バックエンド構成:

```sh
localapp add 5173 --app myapp                            # https://myapp.<ドメイン>/
localapp add 8000 --app myapp --service api --path /api  # https://myapp.<ドメイン>/api/*
```

dev サーバーを止めても登録は消えない（`down` 表示になる）。ポートを変えて再登録すれば上書きされる。
デーモンの状態は `localapp status`、ログは `localapp logs -f` で確認できる。

## Coding Agent 連携

Claude Code / Codex に SKILL を配置すると、エージェントが dev サーバー起動時に自動で登録し、固定 URL でプレビューを案内するようになる。

```sh
localapp skill install claude     # ~/.claude/skills/localapp/
localapp skill install codex      # ~/.codex/skills/localapp/
localapp skill install claude --project   # リポジトリ配下 .claude/skills/ に配置
```

## 比較

**vs. [portless](https://github.com/vercel-labs/portless)**（Vercel Labs）—
最も近い存在で、dev サーバーへの名前付き HTTPS URL・常駐デーモン・AI エージェント対応という
点は共通する。モデルが違う。portless はコマンドのラップが中心（`portless myapp pnpm dev` で
子プロセスに `PORT` を払い出す。既存サーバーは `alias` で登録）で、一部ブラウザが特別扱いする
`.localhost` に依存する（Safari は `/etc/hosts` 経由）。localapp は実 DNS サーバーが中心で、
任意のドメインが curl 含む `getaddrinfo` 経由すべて・全ブラウザで解決でき、hosts の書き換えも
ない。加えてパスマウントで同一オリジンにでき CORS を消せる、CA は Name Constraints 付き、
制御プレーンは Unix socket 上の curl 可能な JSON API。portless が勝る点: Windows / Linux
対応済み、LAN/mDNS モード。localapp は単一静的 Go バイナリで、現状 macOS のみ。

**vs. Caddy + dnsmasq + mkcert** — 定番の手組みスタックは 3 ツール・3 設定 + アプリごとの
証明書配線が必要。localapp は 1 バイナリ・`sudo localapp install` 1 回で、アプリごとの設定はない。

## アンインストール

```sh
sudo localapp uninstall
```

resolver 設定・キーチェーンの CA・常駐登録・状態ディレクトリをすべて撤去する。

## 環境変数

通常は不要。`install` 時に指定した値は常駐設定に永続化される。

| 変数 | 既定値 |
|---|---|
| `LOCALAPP_DOMAIN` | `localapp` |
| `LOCALAPP_DNS_PORT` | `15353` |
| `LOCALAPP_HTTP_PORT` / `LOCALAPP_HTTPS_PORT` | `80` / `443` |
| `LOCALAPP_STATE_DIR` | `/usr/local/var/localapp` |
| `LOCALAPP_SOCKET` | `<state>/control.sock` |
