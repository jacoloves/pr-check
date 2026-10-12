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

### レビューとコード行コメントの数え方

GitHub では、レビューを1回提出すると「レビュー本体」と「そのレビューに属するコード行コメント N 件」が同時にできます。両方を数えると1回のレビューが N+1 件になってしまうため、**1回のレビュー提出を1件**と数えます。

| 対象 | 数え方 |
|------|--------|
| 提出済みレビュー本体 | 1件 |
| 提出済みレビューに属するコード行コメント | 数えない（レビュー本体に含める） |
| レビューに属さない／属先が不明なコード行コメント | 1件ずつ数える |
| 未提出（pending）レビューに属するコード行コメント | 1件ずつ数える |
| スレッドへの返信 | GitHub 上では1つのレビューとして作られるため、返信1回で1件 |

迷うものは「多めに数える」側に倒しています（書きかけのレビューの行コメントを捨てると、自分の最終アクション時刻が失われるため）。

例: alice が1回のレビューで行コメントを3件付けた場合、要対応は 4 ではなく **1** になります。

## 前提条件

- Go 1.26 以上（ビルドする場合）
- [`gh`](https://cli.github.com/) がインストール済みで、`gh auth status` が成功すること
- 対象リポジトリへの閲覧権限があること
- 1 リポジトリあたり、オープンな PR を最大 200 件まで取得します(`gh pr list --limit 200`)。
- `gh` の呼び出しは、リポジトリごとに一覧取得（`gh pr list`）1 回、PR 1 件ごとに 3 回（`gh pr view`、REST のレビュー一覧、REST のコード行コメント一覧）です。レビューは行コメントとの紐づけに数値 ID が必要なため、`gh pr view` ではなく REST（`repos/{repo}/pulls/{n}/reviews`）から取得します。

## 依存ライブラリ

ビルド時に `go build` が自動で取得します(個別のインストールは不要です)。

| ライブラリ | 用途 |
|-----------|------|
| `gopkg.in/yaml.v3` | 設定ファイル(YAML)の読み込み |
| `github.com/mattn/go-runewidth` | 表の列幅を揃えるための、全角文字を含む表示幅の計算 |

## インストール・ビルド

### `go install` で入れる(別PCへの導入におすすめ)

```sh
# インストール($(go env GOPATH)/bin、通常は ~/go/bin に pr-check が入る)
go install github.com/jacoloves/pr-check@latest

# PATH に通っていない場合(コマンドが見つからないとき)
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.zshrc

# 設定ファイルを作る(詳細は「設定ファイル」を参照)
mkdir -p ~/.config/pr-check
curl -fsSL https://raw.githubusercontent.com/jacoloves/pr-check/master/config.example.yaml \
  -o ~/.config/pr-check/config.yaml
$EDITOR ~/.config/pr-check/config.yaml

pr-check
```

- 更新するときも同じ `go install ...@latest` を実行します。
- Go のバージョンが `go.mod` の指定(1.26.8 以上)より古いと、インストールに失敗します。

#### GitHub Enterprise を使う場合

`gh` は接続先を `GH_HOST` 環境変数で決めます。`gh` の既定ホストが github.com のままだと、`pr-check` も github.com に問い合わせてしまい、404 などで失敗します。その場合は接続先ホストを指定してください。

```sh
gh auth status                                # 対象ホストに Logged in か確認
GH_HOST=github.example.com pr-check           # 1回だけ指定して試す
echo 'export GH_HOST=github.example.com' >> ~/.zshrc   # 常用するなら
```

### ソースからビルドする

```sh
git clone https://github.com/jacoloves/pr-check.git
cd pr-check
go build -o pr-check .
```

## 設定ファイル

`-config` を省略した場合、次の順で探索し、**最初に見つかったもの**を使います。

| 優先 | パス | 備考 |
|------|------|------|
| 1 | `./config.yaml` | カレントディレクトリ |
| 2 | `$XDG_CONFIG_HOME/pr-check/config.yaml` | `XDG_CONFIG_HOME` が設定されている場合のみ |
| 3 | `~/.config/pr-check/config.yaml` | ホームディレクトリが判明している場合のみ |

どれも見つからない場合はエラーになります。
`-config` で明示したパスが存在しない場合も、他の場所は探索せずエラーになります。
使用した設定ファイルは、起動時に標準エラー出力へ `設定ファイル: <パス>` と表示されます。

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
# 探索順(カレントディレクトリ → XDG_CONFIG_HOME → ~/.config)で見つかった設定ファイルを使う
./pr-check

# どのディレクトリからでも実行できる(~/.config/pr-check/config.yaml を使用)
cd /tmp && /path/to/pr-check

# 設定ファイルを指定する
./pr-check -config /path/to/config.yaml

# 直近7日以内に更新された PR だけを表示する(詳細は「更新日時で絞り込む」を参照)
./pr-check --since 7d

# 要対応が多い順に並べる(詳細は「並べ替え」を参照)
./pr-check --sort pending

# 色を付けずに出力する(詳細は「色付け」を参照)
./pr-check --color never

# ビルドせずに実行する
go run .
```

## 並べ替え(`--sort` / `--reverse`)

`--sort` で表の並び順を指定します。省略した場合は設定ファイルの `repositories` の順(取得順)のままです。同順位のものは、表の下の「同順位のとき」の基準で並べます。

```sh
./pr-check --sort pending            # 要対応が多い順
./pr-check --sort updated --reverse  # 更新が古い順(updated の並びを反転)
```

| 値 | 並び順 | 同順位のとき |
|----|--------|--------------|
| `pending` | 要対応が多い順 | 更新が新しい順 |
| `updated` | 更新が新しい順 | リポジトリ名、PR番号の昇順 |
| `review` | レビューの緊急度順(`CHANGES_REQUESTED` → `REVIEW_REQUIRED` → `NONE` → `APPROVED`) | 要対応が多い順 |
| `repo` | リポジトリ名の昇順 | PR番号の昇順 |
| `number` | PR番号の昇順 | リポジトリ名の昇順 |

- `--reverse` は `--sort` で決めた並び順を**反転**します。`--sort` なしで指定するとエラーになります。
- 上記以外の `--sort` の値はエラーになり、指定できる値が表示されます。
- `review` では、未知のレビュー判定の値は最後尾に並びます。
- 複数リポジトリを指定している場合、並べ替えは全リポジトリの結果をまとめた上で行います。

## 色付け(`--color`)

表を ANSI カラーで色付けします。`--color` の値で有効/無効を切り替えます。

```sh
./pr-check --color never      # 色を付けない
./pr-check --color always     # パイプ先(less -R など)でも色を付ける
NO_COLOR=1 ./pr-check         # auto のとき、環境変数で無効にする
```

| 値 | 動作 |
|----|------|
| `auto`(既定) | 標準出力が端末のときだけ色を付ける。`NO_COLOR` が空でなければ付けない。`TERM=dumb` のときも付けない |
| `always` | 常に色を付ける。`NO_COLOR` は無視する |
| `never` | 色を付けない |

色の割り当て:

| 対象 | 見た目 |
|------|--------|
| ヘッダ行 | 太字 |
| レビュー `APPROVED` | 緑 |
| レビュー `CHANGES_REQUESTED` | 赤 |
| レビュー `REVIEW_REQUIRED` | 黄 |
| レビュー `NONE` | 薄く表示 |
| 要対応 1 件以上 | 太字の黄 |
| 要対応 0 件 | 薄く表示 |

- 色の有無で列の位置はずれません(列幅は装飾を除いた文字列で計算しています)。
- 列幅は**表示幅**で計算します。日本語などの全角文字は 2 桁、半角文字は 1 桁として数えるため、タイトルに全角文字が含まれていても列が揃います。表示幅の計算には [`go-runewidth`](https://github.com/mattn/go-runewidth) を使っています。
- 記号(例: `○` `※`)のような「幅が曖昧な文字」は、環境変数 `RUNEWIDTH_EASTASIAN` やロケールによって 1 桁にも 2 桁にも数えられます。端末の表示と食い違って列がずれる場合は、`RUNEWIDTH_EASTASIAN=0` または `1` を設定して試してください。
- 上記以外の値はエラーになります。

## 更新日時で絞り込む(`--since`)

`--since` を指定すると、指定した期間内に更新された PR だけを表示します。判定には表の「最終更新」列と同じ PR の `updatedAt` を使い、閾値と**ちょうど同じ時刻の PR も含めます**。省略した場合は従来どおり全件を表示します。

```sh
./pr-check --since 7d            # 7日以内に更新された PR
./pr-check --since 2w            # 2週間以内
./pr-check --since 36h           # 36時間以内
./pr-check --since 90m           # 90分以内
./pr-check --since 2026-10-01    # 2026-10-01 00:00(ローカルタイム)以降
./pr-check --since 7d --sort pending   # 他のオプションと併用できる
```

| 形式 | 例 | 意味 |
|------|----|------|
| `Nd` | `7d` | 暦の上での N 日前。1 以上、上限は 36500 日(約100年) |
| `Nw` | `2w` | 暦の上での N 週間前。`Nd` と同じ範囲で指定する |
| 時間・分など | `36h` / `90m` / `1h30m` | Go の `time.ParseDuration` 形式。0 以下はエラー |
| 日付 | `2026-10-01` | ローカルタイムの 00:00 以降 |

補足:
- 値が解釈できない場合は、`gh` を呼ぶ前にエラーで終了します。エラーには指定できる形式が表示されます。
- 未来の日時を指定すると、標準エラー出力に警告を出して続行します(結果は 0 件になります)。
- 期間外の PR は詳細の取得(`gh pr view` など)も行わないため、`gh` の呼び出し回数が減ります。
- 該当する PR が 0 件のときは、エラーにはならずヘッダ行のみを表示します。

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
| `config.yaml` | 設定ファイル(置き場所は「設定ファイル」の探索順を参照) |
| `pr-check-architecture.md` | 設計ドキュメント |

## トラブルシューティング

| 症状 | 原因と対処 |
|------|-----------|
| `設定ファイル ... を読み込めません` | `-config` のパスか、設定ファイルの探索先(`./config.yaml`、`$XDG_CONFIG_HOME/pr-check/config.yaml`、`~/.config/pr-check/config.yaml`)に `config.yaml` があるか確認してください。 |
| `設定ファイル ... が見つかりません` | 探索先のいずれにも `config.yaml` がありません。`~/.config/pr-check/config.yaml` に配置するか、`-config` で指定してください。 |
| 古いバイナリで `open config.yaml: no such file or directory` | 探索機能を含まない古いバイナリです。`go build -o pr-check .` で再ビルドしてください。 |
| `gh ... の実行に失敗しました` | `gh` が未インストール、未ログイン、または対象リポジトリへの権限がありません。`gh auth status` を確認してください。 |
| `JSON のパースに失敗しました` | `gh` の出力形式が想定と異なります。`gh` を最新版に更新してください。 |
