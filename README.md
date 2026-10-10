# pr-check

設定ファイルで指定したリポジトリについて、PR作成者のオープンなPRの**レビュー状況**と**要対応件数**を一覧表示する CLI ツールです。GitHub へのアクセスは [GitHub CLI (`gh`)](https://cli.github.com/) 経由で行います。

## 表示例

```
リポジトリ              PR  タイトル                      レビュー  要対応  最終更新
jacoloves/jiyucho  8   search: ワークスペース横断検索を追加する  NONE  1    2026-08-19 12:52
```

| 列 | 内容 |
|----|------|
| リポジトリ | `owner/repo` 形式 |
| PR | PR番号 |
| タイトル | PRのタイトル |
| レビュー | `reviewDecision`（`APPROVED` / `CHANGES_REQUESTED` / `REVIEW_REQUIRED`）。未設定の場合は `NONE` |
| 要対応 | 自分の最終アクションより後に発生した、他者のアクション数 |
| 最終更新 | PRの最終更新日時（ローカルタイム） |

## 「要対応」の判定ルール

PR に関する以下のイベントを集め、**自分の最終アクションより後**に発生した**他者のイベント数**を要対応件数とします。

- 自分のアクション: コミット、自分のレビュー、自分のコメント、自分のコード行コメント
- 他者のアクション: 他者のレビュー、コメント、コード行コメント
- 自分のアクションが1件も無い場合は、他者の全イベントが対象になります。
- `exclude_users` に指定したユーザー（bot など）のイベントは数えません。

## 前提条件

- Go 1.26 以上（ビルドする場合）
- [`gh`](https://cli.github.com/) がインストール済みで、`gh auth status` が成功すること
- 対象リポジトリへの閲覧権限があること

## インストール・ビルド

```sh
git clone https://github.com/jacoloves/pr-check.git
cd pr-check
go build -o pr-check .
```

## 設定ファイル

カレントディレクトリの `config.yaml` を読み込みます（`-config` で変更可能）。

```yaml
# PRの作成者。"@me" なら gh にログイン中のユーザー。省略時は "@me"。
author: "@me"

# 確認対象のリポジトリ("owner/repo" 形式)。1つ以上必須。
repositories:
  - jacoloves/jiyucho

# 「要対応」に数えないユーザーのログイン名(botなど)。省略可。
exclude_users: []
```

| キー | 必須 | 説明 |
|------|------|------|
| `author` | 任意 | PR作成者。`"@me"` またはGitHubのログイン名。 |
| `repositories` | 必須 | `owner/repo` 形式のリスト。1件以上。 |
| `exclude_users` | 任意 | 要対応に数えないユーザーのログイン名。 |

注意:
- 未知のキー（`repositorys` などのタイプミス）はエラーになります。
- `repositories` の値が `owner/repo` 形式でない場合もエラーになります。

## 使い方

```sh
# カレントディレクトリの config.yaml を使う
./pr-check

# 設定ファイルを指定する
./pr-check -config /path/to/config.yaml

# ビルドせずに実行する
go run .
```

## テスト

```sh
go vet ./...
go test ./...
```

`gh` の呼び出しは `Runner` 型で抽象化しており、テストでは偽物に差し替えるため、ネットワークや認証なしで実行できます。

## ファイル構成

| ファイル | 内容 |
|----------|------|
| `main.go` | 設定の読み込み、要対応の判定、`gh` 経由の情報収集、表示 |
| `main_test.go` | 単体テスト |
| `config.yaml` | 設定ファイル |
| `pr-check-architecture.md` | 設計ドキュメント |

## トラブルシューティング

| 症状 | 原因と対処 |
|------|-----------|
| `設定ファイル ... を読み込めません` | `-config` のパスか、カレントディレクトリに `config.yaml` があるか確認してください。 |
| `gh ... の実行に失敗しました` | `gh` が未インストール、未ログイン、または対象リポジトリへの権限がありません。`gh auth status` を確認してください。 |
| `JSON のパースに失敗しました` | `gh` の出力形式が想定と異なります。`gh` を最新版に更新してください。 |
