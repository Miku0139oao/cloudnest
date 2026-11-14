# Google Drive 多檔並行上傳（Tampermonkey userscript）

檔案：`google-drive-parallel-upload.user.js`（單檔、零依賴、zh-TW 介面）

## 安裝

1. Chrome / Edge 安裝 Tampermonkey。
2. **Chrome 138+ 必要**：`chrome://extensions` → Tampermonkey → 詳細資料 → 開啟「允許使用者指令碼 / Allow User Scripts」。
3. Tampermonkey 儀表板 → 新增腳本 → 貼上 `google-drive-parallel-upload.user.js` 全文 → 儲存（Ctrl+S）。
4. 開啟 `https://drive.google.com/`，右下角會出現「Drive 並行上傳」面板（可收合）。

## 功能

- **多檔並行**：同時上傳 1–16 個檔案（設定可調），每個檔案各自一條 resumable session。
- **單檔大分塊**：8–128 MiB，減少請求往返；可暫停／繼續／取消／重試。
- **分片模式**：單一大檔切成 N 片並行上傳，完成後自動上傳 `.gdp-manifest.json`；內建「合併下載」還原成單一檔案。
- **資料夾遞迴**：拖入資料夾或「選擇資料夾」，會先建立相同的子資料夾結構。
- **拖放攔截（選用，預設關）**：開啟後把檔案拖到 Drive 頁面任何地方都由本腳本接手。
- **續傳與重試**：308 部分完成、429/5xx 指數退避、session 失效自動重開、暫停後用 `Content-Range: bytes */total` 探測正確位移。
- **測試連線**：只建立 session、不送任何 chunk（不會產生檔案），回報端點、Location/Range 是否可讀。

## 兩種模式（重要）

Drive 的 resumable 協定**不允許同一個檔案用多條連線同時送分塊**（平行送會 503；Google issuetracker 220523936 自 2022 起仍未實作）。Drive 也**沒有伺服器端合併** API（沒有 S3 式 multipart，也沒有 GCS 的 `compose`）。

- **一般模式**：一個檔案 = 一條循序連線；「多線程」來自多個檔案同時上傳。
- **分片模式**：把單一大檔切成 N 個獨立檔案並行上傳 → 真正讓單一大檔吃到多連線；代價是 Drive 上會看到 N 個分片檔＋manifest，**無法直接預覽播放**，其他工具看到的是碎片。需要時用面板的「合併下載」在本機還原（File System Access API，需 Chrome/Edge）。

## 快速驗證

1. 面板 ⚙ → 「測試連線」：應顯示 `v3` / `v2internal` 的 HTTP 狀態與 `Location`、`Range` 是否可讀。
2. 拖 1 個小檔到面板 → 完成後按「重新整理」→ Drive 看得到檔案。
3. 一次拖 20 個檔、併發設 4 → DevTools → Network 過濾 `upload`，Waterfall 應同時有 4 條 `PUT`；改成 1 應變成嚴格接續。
4. 續傳：上傳大檔途中按暫停 → 等幾秒 → 繼續；或 Network 設 Offline 幾秒再恢復。
5. 分片：勾「分片模式」拖 1 GB 檔 → 出現 N 片＋manifest → 完成後「合併下載」→ 比對大小與 SHA-256。
6. 攔截拖放關閉時，把檔案拖到 Drive 頁面空白處 → 仍由 Drive 原生上傳（代表完全沒插手）。

## 疑難排解

| 症狀 | 處理 |
|---|---|
| 面板完全沒出現 | 確認 Tampermonkey 有啟用腳本、Chrome「Allow User Scripts」已開；F12 Console 找 `[GDP]` |
| 測試連線 401 | 未登入或讀不到 `SAPISID` cookie（追蹤保護可能擋掉第一方 cookie）；重新登入 Google |
| `Location` 讀不到 | 大檔會失敗；≤5 MB 會自動改用單次 multipart。可在設定填 API key 或切換端點 |
| 403 `storageQuotaExceeded` | 雲端硬碟空間不足，佇列會自動暫停 |
| 合併下載失敗 | 需 Chrome/Edge；若 Drive 下載端點回 HTML 確認頁，改用手動下載分片再合併 |
| 上傳到錯誤帳號 | 網址需含 `/u/N`；本腳本以網址推 `x-goog-authuser`，沒有切換 UI |

## 已知限制

- 重新整理頁面後無法續傳：session 還在，但瀏覽器已拿不到原本的 `File` 物件。
- 每個 Drive 分頁有各自的佇列；設定經 `localStorage` 共用。
- 共用雲端硬碟會帶 `supportsAllDrives=true`，但沒有專屬權限 UI。
- 分片檔案在 Drive 上不是單一檔案；刪除／搬移要連 manifest 一起處理。

## 隱私

只對 Google 網域發出請求。`SAPISID` 僅用於計算 `SAPISIDHASH`，不會被記錄或外傳。設定（含選填的 API key）存在該頁面的 `localStorage`。
