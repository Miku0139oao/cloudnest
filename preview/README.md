# Cloudnest 雲栖 — UI 預覽

純靜態、零依賴、可離線開啟的 Material 3 預覽。用來確認視覺與互動方向，確認後同一份 `styles/tokens.css` 會原封不動搬進 Wails v3 + Svelte 5 的 `frontend/src/`。

## 開啟方式

直接雙擊 `index.html`（Chrome / Edge），或在專案根目錄：

```powershell
Start-Process .\preview\index.html
```

不需要伺服器、不需要 npm install、不連網。

## 路由

`#<route>?lang=zh-Hant|en&theme=light|dark&state=a,b&static=1`

| Route | 畫面 |
|---|---|
| `home` | 首頁 |
| `onboarding` | 首次啟動 |
| `wizard/1` `wizard/2` `wizard/3` | 備份嚮導三步 |
| `wizard/progress` | 備份進行中 |
| `clouds` | 雲端帳號 |
| `clouds/add` | 新增雲端（右側 sheet） |
| `clouds/connect` `clouds/oauth` | OAuth 連接／等待授權 |
| `files` | 檔案雙欄 |
| `files/restore` | 切片還原對話框 |
| `activity` | 進度（`state=paused` 顯示暫停卡；`state=split` 顯示切片進度） |
| `settings` | 設定（`state=advanced` 直接顯示進階分區） |
| `error/engine` | 引擎錯誤（預留） |

`state` 支援：`selected`、`dragover`、`paused`、`split`、`mirror`、`advanced`、`empty`。
`static=1` 會關閉所有動畫，供截圖使用。

## 可互動的部分

- 左側導航、語言（中／EN）、明暗切換（同步 `color-scheme` 與原生 UI）
- 嚮導：下一步／上一步、來源與雲端帳號選擇、鏡像模式（未勾選同意時「開始備份」停用）、進階展開、大檔案加速開關
- 對話框：原生 `<dialog>`，支援 Esc、點背景關閉、焦點圈限與返回焦點
- 雲端：新增雲端 sheet、provider 分層、OAuth 等待狀態
- 檔案：列選取（原生 checkbox）、切片列 → 一般「下載」自動變成「還原」、還原對話框
- 設定：語言、外觀、開關

## 截圖

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\shots.ps1
```

輸出在 `shots/`（1440×900 @2x = 2880×1800 PNG）。腳本使用一次性 Chrome profile，不會干擾已開啟的瀏覽器。

## 檔案

```
index.html            外殼 + inline SVG sprite + 腳本載入
styles/tokens.css     M3 紫色 token（light/dark）— 之後直接沿用
styles/shell.css      視窗框、標題列、導航列、原生對話框基底
styles/components.css 按鈕、卡、chip、表格、輸入、dialog、進度、骨架
styles/screens.css    各畫面版面
js/icons.js           Material Symbols（@iconify-json/material-symbols，24×24）
js/i18n.js            繁中／英文字典 + 大小/時間格式化
js/fixtures.js        固定假資料（now = 2026-11-14 14:32）
js/ui.js              HTML builders + 品牌 SVG
js/screens/*.js       各畫面
js/main.js            路由、外殼、事件委派、原生 dialog 開啟
```

## 與正式 App 的對應

- `styles/tokens.css` → `frontend/src/tokens.css`
- `js/i18n.js` 的 key → `frontend/src/lib/i18n/{zh-Hant,en}.ts`
- `js/fixtures.js` → `frontend/src/lib/services/mock.ts`
- 各 `js/screens/x.js` → `frontend/src/views/x/*.svelte`（hash 語法相同）

## 注意

- 所有資料都是假的；沒有呼叫 rclone、沒有網路請求。
- 品牌圖示是簡化的手繪 SVG（Google Drive、OneDrive、Dropbox、Box、MEGA、pCloud、S3、WebDAV），僅供預覽。
- 這個預覽使用原生 `<dialog>`、原生 radio/checkbox/switch 與 `accent-color`，正式 App 會沿用同樣的語意。
