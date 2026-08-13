# localapp 開発ガイド

ローカル開発用の DNS + リバースプロキシ常駐デーモン。各アプリに `https://<app>.<ドメイン>` 形式の固定 URL を与える。単一 Go バイナリにデーモンと CLI を同居させる。

## 実装状況（2026-08-13 時点）

- **macOS 版は実装完了**（TODO.md の Phase 0〜5）。全テスト・E2E 検証済み
- 残タスクは [TODO.md](TODO.md)（Linux 対応、実機検証、改善候補）
- 開発ドメインは `install --domain` で指定可能。plist の `EnvironmentVariables` に永続化される

## ドキュメント構成

| ファイル | 内容 |
|---|---|
| [README.md](README.md) | 利用者向け。インストール・使い方・トラブルシュート |
| [DESIGN.md](DESIGN.md) | 設計と考慮事項。API 仕様・ルーティング・TLS・セキュリティ・プラットフォーム抽象 |
| [TODO.md](TODO.md) | 残タスク |
| `skills/localapp/SKILL.md` | Coding Agent 向けスキル（`go:embed` でバイナリに埋め込み） |

## パッケージ構成

```
cmd/localapp/        CLI エントリポイント（flag + switch。フレームワーク不使用）
internal/config/     環境変数の解決（LOCALAPP_*）
internal/registry/   登録簿。App > Service、registry.json への atomic write
internal/control/    Control Plane API（HTTP+JSON over Unix socket）+ CLI 用クライアント
internal/proxy/      リバースプロキシ。ルーティング・WS 透過・死活エラーページ
internal/dnsd/       DNS サーバー（miekg/dns。唯一の外部依存）
internal/ca/         ルート CA（Name Constraints 付き）+ SNI オンデマンド発行
internal/dashboard/  apex の一覧ページ（html/template、読み取り専用）
internal/scan/       未登録 LISTEN ポートの検出
internal/skill/      SKILL.md の埋め込みと配置（claude / codex）
internal/platform/   OS 依存の隔離層。コアは platform を import しない
```

## 開発コマンド

```sh
go build ./... && go vet ./... && gofmt -l . && go test ./...
```

E2E（sudo 不要）の定型:

```sh
S=$(mktemp -d)   # 注意: Unix socket のパス長制限（macOS 約 104 バイト）。深いパスを使わない
LOCALAPP_STATE_DIR=$S LOCALAPP_DOMAIN=myorg \
LOCALAPP_HTTP_PORT=18080 LOCALAPP_HTTPS_PORT=18443 LOCALAPP_DNS_PORT=25353 \
  ./localapp daemon &
./localapp add 5173 --json
dig +short @127.0.0.1 -p 25353 app1.myorg A
curl --cacert $S/ca/root.crt --resolve app1.myorg:18443:127.0.0.1 https://app1.myorg:18443/
```

- **実機にインストール済みのデーモンが 80/443/15353 を使用している**ことがある。E2E は必ず代替ポートを使う
- 実機の状態確認: `localapp status` / `ps aux | grep "localapp daemon"` / `/etc/resolver/` / `/Library/LaunchDaemons/dev.localapp.plist`

## コーディング規約

- 外部依存は `miekg/dns` のみ。追加しない
- OS 依存コード（resolver 登録・CA 信頼・サービス常駐・ブラウザ起動）は `internal/platform/` にのみ置く。コアパッケージは platform を import しない
- CLI: exit code 0/1/2、データは stdout、診断は stderr。`--json` 出力は API レスポンスそのままで独自形式を作らない。スキーマの破壊的変更禁止
- コード内のコメント・CLI メッセージ・SKILL.md は英語（OSS 公開のため全面英語化済み）。DESIGN.md / CLAUDE.md / TODO.md / README.ja.md は日本語を維持
- セキュリティ上の不変条件（変更時は DESIGN.md「セキュリティ」を必ず参照）:
  - 全リスナーは `127.0.0.1` bind
  - 証明書発行は設定ドメイン配下のみ（CA の Name Constraints + コード検証の二重防御。テスト必須）
  - SNI はファイル名に使うため発行前に検証（パストラバーサル対策）
  - 変更系 API は Unix socket のみ。ダッシュボードは読み取り専用
  - ドメインは `local` / `localhost` とその配下を拒否（mDNS / RFC 6761。`.local` 配下は apex が mDNS に取られる実害を確認済み）

## 整合性ルール

CLI のコマンド・フラグを変更したら、以下を同時に更新する。

1. DESIGN.md の「CLI」表・「Control Plane API」の CLI 対応表
2. `skills/localapp/SKILL.md`（**ドメイン名を固定しない**。URL は API の `urls` を使わせる）
3. `cmd/localapp/main.go` のヘルプ
4. README.md の使い方

設計に関わる変更はコードより先に DESIGN.md を更新する。
