# Idle Lineage Launcher

這是一款適用於《放置天堂》的桌面啟動器，使用 Wails v3 與 React TypeScript 製作。

《放置天堂》由「秋玥」製作，相關內容分享於[巴哈姆特](https://forum.gamer.com.tw/C.php?bsn=84452&snA=8362&tnum=2953)以及 [GitHub](https://github.com/shines871/idle-lineage-class)。

這個 Launcher 的主要目標是打包 Git 更新流程，提供各位一鍵下載離線版，同時可以一鍵透過瀏覽器開啟遊戲。

## 主要功能

- 下載並安裝官方遊戲
- 開啟 Launcher 時自動檢查一次，之後可隨時手動檢查更新
- 由使用者決定何時更新
- 使用系統預設瀏覽器開啟遊戲
- 顯示下載與更新進度

## 支援的電腦版本

- macOS：需要 macOS 12 或更新版本。
  - Intel 晶片的 Mac 請下載檔名結尾為 `-amd64.dmg` 的版本。
  - 配備 Apple 晶片的 Mac（M 系列與 MacBook Neo）請下載檔名結尾為 `-arm64.dmg` 的版本。
- Windows：需要 Windows 10 或更新版本。

## 使用方式

1. 開啟啟動器。
2. 尚未安裝遊戲時，點選「下載遊戲」。
3. 有新版本時，點選更新即可同步至官方最新版本。
4. 點選「啟動遊戲」，使用系統預設瀏覽器遊玩。

或著，請看操作示範影片

<video src="https://www.youtube.com/watch?v=lSfKQv8IMv4" width="320" height="240" controls></video>

[![How to Use](https://img.youtube.com/vi/lSfKQv8IMv4/0.jpg)](https://www.youtube.com/watch?v=lSfKQv8IMv4)

## 第一次開啟

由於本工具並未簽名，第一次開啟時，macOS 與 Windows 會顯示各自的安全性警告，請依照對應系統的提示完成處理。

- macOS
<img width="720" height="632" alt="mac-security" src="https://github.com/user-attachments/assets/7306a982-159e-4c44-b2af-3d422ed010d3" />

- Windows
<img width="872" height="812" alt="Large GIF (872x812)" src="https://github.com/user-attachments/assets/19bb8899-e5ba-42f8-aef4-ba1a2b9972d1" />

## 免責聲明

- 本工具不負責保管遊戲進度；遊戲進度由您的瀏覽器負責保存與管理。
- 離線版與線上版的遊戲進度彼此獨立，兩者皆由瀏覽器負責保存與管理。
- 《放置天堂》使用離線版可獲得最佳遊戲體驗；本工具主要提供給不熟悉程式操作的玩家，方便一鍵更新離線版本。
- 若不熟悉 Git，不建議修改本工具下載的離線版遊戲檔案。

## 注意事項

- 更新會以官方版本覆蓋遊戲檔案，請勿在遊戲目錄中存放個人檔案。
- Launcher 只會在開啟時自動檢查一次更新。程式持續開啟期間不會再次自動檢查，請手動點選「檢查更新」。
- 遊戲存檔由瀏覽器管理；更換瀏覽器或瀏覽器設定檔後，可能無法看到原本的存檔。
- 遊戲已開啟時，更新後需重新整理頁面或重新啟動遊戲。

## 開發環境

需要安裝以下工具：

- Go 1.25 以上版本
- Node.js 22 以上版本
- Wails CLI v3.0.0-alpha.97（指令名稱為 `wails3`）
- Task

第一次執行時，先在專案根目錄安裝 Go 與前端依賴：

```sh
go mod download
npm install --prefix frontend
```

啟動開發模式：

```sh
task dev
```

這個指令會啟動 Wails 開發模式與前端開發伺服器。

第三方聲明請參閱 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
