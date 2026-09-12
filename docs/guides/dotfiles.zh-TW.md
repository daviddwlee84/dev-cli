---
description: 辨識 chezmoi 設定、選擇平台適用的 dotfiles、委派原生操作，並檢查指定 fleet 主機。
authority: project
status: stable
verified_on: 2026-09-11
lang: zh-TW
---

# Dotfiles

`dev dotfile` 是選用的 chezmoi 入口。Chezmoi 管理來源、template 與部署；
dotfiles repository 管理 prompts 和套件安裝。Dev 負責定位設定、引導 setup，
以及委派使用者明確要求的原生操作，不要求安裝作者的 dotfiles。

## Setup 前先檢查

```bash
dev dotfile
dev dotfile status --json
dev dotfile status --chezmoi-config /path/to/chezmoi.toml
```

Status 只讀設定與本機 Git identity，不啟動 chezmoi、不展開 templates、不執行
hooks，也不 fetch。輸出分開呈現設定的 source directory、`.chezmoiroot` 選出的
source-state directory，以及 Git working tree。來源的 revision 不代表已套用到
HOME；部署差異會明確標示為尚未檢查。

支援 TOML、YAML、JSON 與 JSONC；探索依原生 XDG 搜尋順序，包含次要的設定與資料目錄。多份衝突、無法讀取或格式錯誤的設定保持
unknown，不改用另一個 checkout，也不授權覆蓋既有 setup。
`--chezmoi-config` 選擇本機原生設定，與 dev 的全域 `--config` 不同。
找不到 chezmoi setup 不代表使用者沒有使用其他 dotfile manager。

## 選擇 repository

```bash
dev dotfile setup
dev dotfile setup --repo https://github.com/you/dotfiles.git
dev dotfile setup --preset david
```

互動流程先顯示 repository 與原生初始化命令。非互動模式預設只顯示方案，
必須加 `--yes` 才執行初始化；另外加 `--apply` 才要求初始化後部署。
Dev 的確認不會代答 repository prompts，也不跳過 chezmoi 的衝突處理。
已有來源時保留原來源，不替換成推薦項目。

選用的 `david` preset 依 dev 實際執行環境推薦作者維護的 repository：

| 執行環境 | Repository | 支援程度 |
|---|---|---|
| macOS、一般 Linux、WSL 內部 Linux | [dotfiles](https://github.com/daviddwlee84/dotfiles) | 主要平台 |
| 原生 Windows | [dotfiles-windows](https://github.com/daviddwlee84/dotfiles-windows) | Windows |
| 原生 Android Termux | [dotfiles-Termux](https://github.com/daviddwlee84/dotfiles-Termux) | Experimental |
| iSH | [dotfiles-iSH](https://github.com/daviddwlee84/dotfiles-iSH) | Experimental |
| OpenWrt／ImmortalWrt | [dotfiles-OpenWrt](https://github.com/daviddwlee84/dotfiles-OpenWrt) | Experimental |

`dotfiles-all` 是跨 repository 維護用的 superproject，使用者選擇對應的獨立
repository 即可。Windows host 不等於 WSL，一般 Alpine 不等於 iSH，Android
shell 也不一定是 Termux。環境不明或不支援時提供指引，不猜測可用 desktop
安裝流程。Experimental 推薦也不保證所有架構都有可執行的 dev binary。

缺少 chezmoi 時，dev 顯示安裝與 repository bootstrap 指引。特殊平台保留原生
bootstrap 入口；dev 不下載並執行 bootstrap script，也不替使用者選套件管理器。

## 明確委派原生操作

```bash
dev dotfile diff
dev dotfile apply
dev dotfile update
```

Diff 與 apply 接受 target paths。Apply 套用目前來源；update 使用 chezmoi
自己的更新行為，包含設定中的更新命令與後續部署。原生 prompts、輸出和失敗
保持可見。`chezmoi edit`、`add`、`re-add` 等進階操作仍直接使用 chezmoi。

這些明確操作可能展開 template 或執行 hooks；apply／update 也可能執行
repository scripts 和安裝套件。原生 dry-run 不是 sandbox，因為
[chezmoi hooks 在 dry-run 仍會執行](https://www.chezmoi.io/reference/configuration-file/hooks/)。
Dev 不快取展開後的 diff，也不承諾回滾任意 script。

## 檢查其他機器

```bash
dev fleet dotfile status --host lab
dev fleet dotfile status --host lab --host winlab --json
```

每台指定主機透過有大小限制與版本的唯讀 helper，讀取該使用者的慣例設定。
不啟動 chezmoi，也不載入 repository／runtime inventory。遠端缺少或不相容的
dev，與缺少 chezmoi 分開回報。本機設定路徑不傳給遠端；第一版不提供遠端
apply，也不傳送 HOME 或設定內容。

[FLEET 主機選單](remote-fleet.md#dashboard-host-tree) 提供相同的 status 入口，
不需要先選 repository。

## 既有 helpers 與責任邊界

作者原有的 `fleet chezmoi` 繼續保有自己的 inventory 與行為。Dev 不匯入其
machine list，也不改寫其 apply／update 語意。`dev fleet sync` 仍同步符合條件的
Git branch；`dev fleet files` 仍只處理明確匯出的 ignored files。

`dotcfg` 是作者 repository 專用的 prompt 編輯器；appsrc 診斷 app 的安裝來源。
兩者維持選用的獨立 helper。Dotfiles 可以把 dev 當套件安裝，但 bootstrap 與
原生日常操作持續不依賴 dev，避免形成必要的循環依賴。
