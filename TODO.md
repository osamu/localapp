# localapp 残タスク

設計は [DESIGN.md](DESIGN.md)、実装状況は [CLAUDE.md](CLAUDE.md) を参照。
macOS 版（Phase 0〜5）は実装・E2E 検証済み。

## Linux 対応（Phase 6）

ゴール: Ubuntu（systemd-resolved 前提）で macOS 版相当が動作する。

- [ ] `platform_linux.go` — resolver: `/etc/systemd/resolved.conf.d/localapp.conf`（`DNS=127.0.0.1:<port>` + `Domains=~<ドメイン>`）→ `systemctl restart systemd-resolved`
- [ ] CA 信頼: `update-ca-certificates`（Debian 系）/ `trust anchor`（RHEL 系）の分岐
- [ ] systemd unit 生成 + `AmbientCapabilities=CAP_NET_BIND_SERVICE`（非 root 常駐）。`Environment=` へ非既定設定を永続化（darwin の plist `EnvironmentVariables` と同等）
- [ ] 状態ディレクトリ `/var/lib/localapp`
- [ ] resolved 非搭載環境（素の `/etc/resolv.conf`）の検出とエラー出力（初版は非対応と明示）
- [ ] 検証: Linux VM / コンテナで install → https 疎通 → uninstall

## 改善候補

- [ ] `LOCALAPP_STATE_DIR` が長すぎる場合のエラーメッセージ改善（Unix socket の macOS 約 104 バイト制限。現状は `bind: invalid argument` がそのまま出る）
- [ ] `localapp open`（app 省略時）でダッシュボードを開く
- [x] リリース手順の整備 — `v*` タグ push で GitHub Release を自動作成（`.github/workflows/release.yml`。darwin arm64/amd64 バイナリ + checksums、version は ldflags 埋め込み）

## 対象外（実装しない。設計上の排除もしない）

- registry.json 手編集 + fsnotify リロード
- `GET /v1/events`（SSE）
- launchd ソケットアクティベーションによる macOS 非 root 化
- プロジェクト設定ファイル（`.localapp.json`）
- goreleaser によるバイナリ配布 / Homebrew tap
