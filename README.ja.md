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

## Coding Agent 連携

Claude Code / Codex に SKILL を配置すると、エージェントが dev サーバー起動時に自動で登録し、固定 URL でプレビューを案内するようになる。

```sh
localapp skill install claude     # ~/.claude/skills/localapp/
localapp skill install codex      # ~/.codex/skills/localapp/
localapp skill install claude --project   # リポジトリ配下 .claude/skills/ に配置
```

## トラブルシュート

| 現象 | 対処 |
|---|---|
| Vite が `Blocked request` を返す | `server.allowedHosts: ['.<ドメイン>']` |
| HMR が接続できない | `server.hmr: { clientPort: 443, protocol: 'wss' }` |
| Next.js が cross-origin を警告 / 拒否 | `next.config` の `allowedDevOrigins` に `.<ドメイン>` を追加 |
| Node / curl が証明書エラー | `NODE_EXTRA_CA_CERTS=$(localapp ca path)` / `SSL_CERT_FILE=$(localapp ca path)` |
| Firefox のみ証明書エラー | Firefox は独自トラストストアを持つため `certutil -A` で手動登録 |
| アドレスバーで検索になってしまう | `https://` を付けて入力するか `localapp open <app>` を使う |
| Docker コンテナから解決できない | `--add-host <app>.<ドメイン>:host-gateway` + CA をコンテナへ配置 |
| Go / Node スクリプトから解決できない | 仕様（名前解決は `getaddrinfo` 経由のみ）。プログラム間通信は `localhost:PORT` を使う |

デーモンの状態は `localapp status`、ログは `localapp logs -f` で確認できる。

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
