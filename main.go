package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ==========================================
   CONFIG & UPDATE SYSTEM (ORIGINAL)
========================================== */
const AppVersion = "1.0"
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

// BATAS MAX BARIS AGAR TIDAK LEMOT/CRASH
const MaxScrollback = 100 

var currentDir string = "/sdcard" 
var activeStdin io.WriteCloser
var cmdMutex sync.Mutex

// --- VARIABEL TAMBAHAN GAME TOOLS ---
const AccountFile = "/sdcard/akun.ini"
const OnlineAccFile = "/sdcard/accml_online.ini"
const UrlConfigFile = "/sdcard/ml_url_config.ini"

var PackageNames = []string{"com.mobile.legends", "com.hhgame.mlbbvn", "com.mobile.legends.usa", "com.mobile.v2l"}
var AppNames     = []string{"Global", "VNG", "USA", "M6"}
var SelectedGameIdx = 0

type OnlineConfig struct {
	Version string `json:"version"`
	Message string `json:"message"`
	Link    string `json:"link"`
}

//go:embed fd.png
var fdPng []byte

//go:embed bg.png
var bgPng []byte

//go:embed driver.zip
var driverZip []byte

/* ==========================================
   SECURITY LOGIC (ORIGINAL)
========================================== */
func decryptConfig(encryptedStr string) ([]byte, error) {
	defer func() { if r := recover(); r != nil {} }()
	key := []byte(CryptoKey)
	if len(key) != 32 { return nil, errors.New("key length error") }
	encryptedStr = strings.TrimSpace(encryptedStr)
	data, err := base64.StdEncoding.DecodeString(encryptedStr)
	if err != nil { return nil, err }
	block, err := aes.NewCipher(key)
	if err != nil { return nil, err }
	gcm, err := cipher.NewGCM(block)
	if err != nil { return nil, err }
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize { return nil, errors.New("data corrupt") }
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil { return nil, err }
	return plaintext, nil
}

/* ==========================================
   TERMINAL LOGIC (ORIGINAL)
========================================== */
type Terminal struct {
	grid         *widget.TextGrid
	scroll       *container.Scroll
	curRow       int
	curCol       int
	curStyle     *widget.CustomTextGridStyle
	mutex        sync.Mutex
	reAnsi       *regexp.Regexp
	needsRefresh bool
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	term := &Terminal{
		grid:         g,
		scroll:       container.NewScroll(g),
		curRow:       0,
		curCol:       0,
		curStyle:     defStyle,
		reAnsi:       re,
		needsRefresh: false,
	}
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		for range ticker.C {
			term.mutex.Lock()
			if term.needsRefresh {
				term.grid.Refresh()
				term.scroll.ScrollToBottom()
				term.needsRefresh = false
			}
			term.mutex.Unlock()
		}
	}()
	return term
}

func ansiToColor(code string) color.Color {
	switch code {
	case "30": return color.Gray{Y: 100}
	case "31": return theme.ErrorColor()
	case "32": return theme.SuccessColor()
	case "33": return theme.WarningColor()
	case "34": return theme.PrimaryColor()
	case "35": return color.RGBA{R: 200, G: 0, B: 200, A: 255}
	case "36": return color.RGBA{R: 0, G: 255, B: 255, A: 255}
	case "37": return theme.ForegroundColor()
	case "90": return color.Gray{Y: 100} // ABU-ABU
	case "91": return color.RGBA{R: 255, G: 100, B: 100, A: 255}
	case "92": return color.RGBA{R: 100, G: 255, B: 100, A: 255}
	case "93": return color.RGBA{R: 255, G: 255, B: 100, A: 255}
	case "94": return color.RGBA{R: 100, G: 100, B: 255, A: 255}
	case "95": return color.RGBA{R: 255, G: 100, B: 255, A: 255}
	case "96": return color.RGBA{R: 100, G: 255, B: 255, A: 255}
	case "97": return color.White
	default: return nil
	}
}

func (t *Terminal) Clear() {
	t.mutex.Lock()
	t.grid.SetText("")
	t.curRow = 0; t.curCol = 0
	t.needsRefresh = true
	t.mutex.Unlock()
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	raw := string(p)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for len(raw) > 0 {
		loc := t.reAnsi.FindStringIndex(raw)
		if loc == nil { t.printText(raw); break }
		if loc[0] > 0 { t.printText(raw[:loc[0]]) }
		t.handleAnsiCode(raw[loc[0]:loc[1]])
		raw = raw[loc[1]:]
	}
	t.needsRefresh = true
	return len(p), nil
}

func (t *Terminal) handleAnsiCode(codeSeq string) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]
	command := codeSeq[len(codeSeq)-1]
	switch command {
	case 'm':
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "" || part == "0" { t.curStyle.FGColor = theme.ForegroundColor() } else { col := ansiToColor(part); if col != nil { t.curStyle.FGColor = col } }
		}
	case 'J':
		if strings.Contains(content, "2") { t.grid.SetText(""); t.curRow = 0; t.curCol = 0 }
	case 'H': t.curRow = 0; t.curCol = 0
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { 
			t.curRow++
			t.curCol = 0
			if len(t.grid.Rows) > MaxScrollback { t.grid.Rows = t.grid.Rows[1:]; t.curRow-- }
			continue 
		}
		if char == '\r' { t.curCol = 0; continue }
		for t.curRow >= len(t.grid.Rows) { t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}}) }
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}
		cellStyle := *t.curStyle
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{Rune: char, Style: &cellStyle})
		t.curCol++
	}
}

/* ===============================
   SYSTEM HELPERS (ORIGINAL - RESTORED)
================================ */
func drawProgressBar(term *Terminal, label string, percent int, colorCode string) {
	barLength := 20; filledLength := (percent * barLength) / 100; bar := ""
	for i := 0; i < barLength; i++ { if i < filledLength { bar += "█" } else { bar += "░" } }
	term.Write([]byte(fmt.Sprintf("\r%s %s [%s] %d%%", colorCode, label, bar, percent)))
}

func CheckRoot() bool {
	cmd := exec.Command("su", "-c", "id -u")
	out, err := cmd.Output()
	if err != nil { return false }
	return strings.TrimSpace(string(out)) == "0"
}

func CheckKernelDriver() bool {
	signature := "read_physical_address"
	cmd := exec.Command("su", "-c", fmt.Sprintf("grep -q '%s' /proc/kallsyms", signature))
	return cmd.Run() == nil
}

func CheckSELinux() string {
	cmd := exec.Command("su", "-c", "getenforce")
	out, err := cmd.Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
}

func RequestStoragePermission(term *Terminal) {
	if term != nil { term.Write([]byte("\x1b[33m[*] Opening Settings: All Files Access...\x1b[0m\n")) }
	pkgName := "com.tangsan.executor"
	cmd1 := exec.Command("sh", "-c", fmt.Sprintf("am start -a android.settings.MANAGE_APP_ALL_FILES_ACCESS_PERMISSION -d package:%s", pkgName))
	if cmd1.Run() != nil {
		if term != nil { term.Write([]byte("\x1b[33m[!] Trying generic settings...\x1b[0m\n")) }
		exec.Command("sh", "-c", "am start -a android.settings.MANAGE_ALL_FILES_ACCESS_PERMISSION").Run()
	}
}

// FUNGSI DOWNLOAD ASLI (TETAP ADA)
func downloadFile(url string, filepath string) (error, string) {
	exec.Command("su", "-c", "rm -f "+filepath).Run()
	cmdStr := fmt.Sprintf("curl -k -L -f --connect-timeout 10 -o %s %s", filepath, url)
	cmd := exec.Command("su", "-c", cmdStr)
	err := cmd.Run()
	if err == nil {
		checkCmd := exec.Command("su", "-c", "[ -s "+filepath+" ]")
		if checkCmd.Run() == nil { return nil, "Success" }
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil { return err, "Init Fail" }
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120.0.0.0")
	resp, err := client.Do(req)
	if err != nil { return err, "Net Err" }
	defer resp.Body.Close()
	if resp.StatusCode != 200 { return fmt.Errorf("HTTP %d", resp.StatusCode), "HTTP Err" }
	writeCmd := exec.Command("su", "-c", "cat > "+filepath)
	stdin, err := writeCmd.StdinPipe()
	if err != nil { return err, "Pipe Err" }
	go func() { defer stdin.Close(); io.Copy(stdin, resp.Body) }()
	if err := writeCmd.Run(); err != nil { return err, "Write Err" }
	return nil, "Success"
}

func createLabel(text string, color color.Color, size float32, bold bool) *canvas.Text {
	lbl := canvas.NewText(text, color)
	lbl.TextSize = size; lbl.Alignment = fyne.TextAlignCenter
	if bold { lbl.TextStyle = fyne.TextStyle{Bold: true} }
	return lbl
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil { return err }
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil { return err }
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

/* ===============================
   HELPER KHUSUS GAME TOOLS
================================ */
func removeFileRoot(path string) {
	exec.Command("su", "-c", "rm -f \""+path+"\"").Run()
}

func cleanString(s string) string {
	s = strings.ReplaceAll(s, "\ufeff", "")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func generateRandomID() string {
	randHex := func(n int) string {
		bytes := make([]byte, n/2)
		if _, err := rand.Read(bytes); err != nil { return "" }
		return hex.EncodeToString(bytes)
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", randHex(56), randHex(4), randHex(4), randHex(4), randHex(12))
}

func runMLBBTask(term *Terminal, taskName string, action func()) {
	term.Write([]byte(fmt.Sprintf("\n\x1b[33m[GAME TOOL] %s...\x1b[0m\n", taskName)))
	go action()
}

func downloadGameConfig(url string, filepath string) error {
	removeFileRoot(filepath)
	cmd := exec.Command("su", "-c", fmt.Sprintf("curl -k -L -f --connect-timeout 20 -o %s %s", filepath, url))
	return cmd.Run()
}

func parseAccountFile(path string) ([]string, []string, []string, error) {
	var content string
	b, err := os.ReadFile(path)
	if err == nil {
		content = string(b)
	} else {
		cmd := exec.Command("su", "-c", "cat \""+path+"\"")
		out, err2 := cmd.Output()
		if err2 != nil {
			return nil, nil, nil, fmt.Errorf("Gagal baca file (Root): %v", err2)
		}
		content = string(out)
	}

	var ids, names, displays []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := cleanString(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") { continue }
		parts := strings.Fields(line)
		if len(parts) >= 1 {
			id := parts[0]
			name := "No Name"
			if len(parts) > 1 { name = strings.Join(parts[1:], " ") }
			ids = append(ids, id)
			names = append(names, name)
			displays = append(displays, name) 
		}
	}
	if len(ids) == 0 { return nil, nil, nil, errors.New("File kosong") }
	return ids, names, displays, nil
}

func applyDeviceIDLogic(term *Terminal, targetID, targetPkg, targetAppName, customAccName string) {
	targetID = cleanString(targetID)
	targetFile := fmt.Sprintf("/data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml", targetPkg, targetPkg)

	term.Write([]byte(fmt.Sprintf("\x1b[36mTarget: %s\nPackage: %s\x1b[0m\n", targetAppName, targetPkg)))
	if customAccName != "" { term.Write([]byte(fmt.Sprintf("\x1b[35mAkun: %s\x1b[0m\n", customAccName))) }

	if exec.Command("su", "-c", fmt.Sprintf("[ -f '%s' ]", targetFile)).Run() != nil {
		term.Write([]byte("\x1b[31m[ERR] Data game belum ada! Login game dulu minimal sekali.\x1b[0m\n"))
		return
	}

	cmds := []string{
		fmt.Sprintf("am force-stop %s", targetPkg),
		fmt.Sprintf("rm -rf /data/user/0/%s/shared_prefs/__gpm__.xml", targetPkg),
		fmt.Sprintf("sed -i 's/.*<string name=\"JsonDeviceID\">.*/    <string name=\"JsonDeviceID\">%s<\\/string>/' %s", targetID, targetFile),
		fmt.Sprintf("sed -i 's/.*<string name=\"__Java_JsonDeviceID__\">.*/    <string name=\"__Java_JsonDeviceID\">%s<\\/string>/' %s", targetID, targetFile),
	}

	for _, cmd := range cmds { exec.Command("su", "-c", cmd).Run() }
	term.Write([]byte("\x1b[32m[SUKSES] ID Berhasil diterapkan.\x1b[0m\n"))
}

/* ==========================================
   HELPER POPUP KHUSUS GAME TOOLS
========================================== */
func showGamePopup(w fyne.Window, overlay *fyne.Container, title string, content fyne.CanvasObject, 
                  btn1Text string, act1 func(), btn2Text string, act2 func(), size fyne.Size) {
    
    // Reset overlay
    overlay.Objects = nil
    
    // Background semi-transparent
    bg := canvas.NewRectangle(color.RGBA{0, 0, 0, 200})
    
    // Main popup container with fixed size
    popupContainer := container.NewVBox()
    
    // Title
    titleLabel := canvas.NewText(title, color.White)
    titleLabel.TextSize = 18
    titleLabel.TextStyle = fyne.TextStyle{Bold: true}
    titleLabel.Alignment = fyne.TextAlignCenter
    
    popupContainer.Add(container.NewCenter(titleLabel))
    popupContainer.Add(widget.NewSeparator())
    
    // Content
    if content != nil {
        popupContainer.Add(container.NewPadded(content))
    }
    
    // Buttons
    buttonsContainer := container.NewHBox()
    
    if btn1Text != "" {
        btn1 := widget.NewButton(btn1Text, func() {
            overlay.Hide()
            if act1 != nil {
                act1()
            }
        })
        btn1.Importance = widget.DangerImportance
        buttonsContainer.Add(container.NewCenter(btn1))
    }
    
    if btn2Text != "" {
        // Add spacer between buttons
        if btn1Text != "" {
            buttonsContainer.Add(layout.NewSpacer())
        }
        
        btn2 := widget.NewButton(btn2Text, func() {
            overlay.Hide()
            if act2 != nil {
                act2()
            }
        })
        btn2.Importance = widget.HighImportance
        buttonsContainer.Add(container.NewCenter(btn2))
    }
    
    popupContainer.Add(layout.NewSpacer())
    popupContainer.Add(buttonsContainer)
    
    // Card container
    card := widget.NewCard("", "", container.NewPadded(
        container.NewVBox(popupContainer),
    ))
    
    // Fixed size wrapper
    wrapper := container.NewCenter(
        container.NewPadded(
            container.NewStack(
                canvas.NewRectangle(color.Gray{Y: 30}),
                container.NewPadded(card),
            ),
        ),
    )
    
    // Set fixed size
    wrapper.Resize(size)
    
    // Center in overlay
    centered := container.NewCenter(wrapper)
    
    overlay.Objects = []fyne.CanvasObject{bg, centered}
    overlay.Show()
    overlay.Refresh()
}

func maskURL(urlStr string) string {
    // Simple URL masking - only show domain
    u, err := url.Parse(urlStr)
    if err != nil {
        return "https://***.***/***"
    }
    
    // Mask path and query parameters
    masked := fmt.Sprintf("https://%s/***", u.Host)
    return masked
}

/* ==========================================
   FUNGSI POPUP UNTUK GAME TOOLS
========================================== */
func showAccountListPopup(w fyne.Window, overlay *fyne.Container, term *Terminal, ids, names, displays []string, isOnline bool) {
    selectedIndex := -1
    
    listWidget := widget.NewList(
        func() int { return len(displays) },
        func() fyne.CanvasObject { 
            icon := widget.NewIcon(theme.AccountIcon())
            lbl := widget.NewLabel("Account Name")
            lbl.TextStyle = fyne.TextStyle{Bold: true}
            return container.NewHBox(icon, lbl)
        },
        func(i int, o fyne.CanvasObject) { 
            box := o.(*fyne.Container)
            lbl := box.Objects[1].(*widget.Label)
            lbl.SetText(displays[i])
            
            if i == selectedIndex {
                lbl.TextStyle.Italic = true
            } else {
                lbl.TextStyle.Italic = false
            }
        },
    )
    
    listContainer := container.NewGridWrap(fyne.NewSize(300, 350), listWidget)
    
    showGamePopup(w, overlay, "DAFTAR AKUN",
        listContainer,
        "BATAL", func() {
            if isOnline {
                removeFileRoot(OnlineAccFile)
            }
        },
        "PILIH", func() {
            if selectedIndex >= 0 {
                runMLBBTask(term, "Login: "+names[selectedIndex], func() {
                    applyDeviceIDLogic(term, ids[selectedIndex], PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], names[selectedIndex])
                    exec.Command("su", "-c", fmt.Sprintf("am start -n %s/com.moba.unityplugin.MobaGameUnityActivity", PackageNames[SelectedGameIdx])).Run()
                    if isOnline {
                        removeFileRoot(OnlineAccFile)
                    }
                })
            } else {
                if isOnline {
                    removeFileRoot(OnlineAccFile)
                }
            }
        },
        fyne.NewSize(350, 450))
    
    listWidget.OnSelected = func(id int) { 
        selectedIndex = id
        listWidget.Refresh()
    }
}

func showURLInputPopup(w fyne.Window, overlay *fyne.Container, term *Terminal) {
    // Create URL input form
    urlEntry := widget.NewEntry()
    urlEntry.SetPlaceHolder("https://example.com/accounts.txt")
    urlEntry.Validator = func(s string) error {
        if s == "" {
            return errors.New("URL cannot be empty")
        }
        if !strings.HasPrefix(s, "http") {
            return errors.New("Must be a valid URL")
        }
        return nil
    }
    
    content := container.NewVBox(
        widget.NewLabel("Masukkan URL akun online:"),
        widget.NewSeparator(),
        urlEntry,
    )
    
    showGamePopup(w, overlay, "INPUT URL", 
        content,
        "BATAL", nil,
        "DOWNLOAD", func() {
            if urlEntry.Text != "" {
                // Save URL for future use
                exec.Command("su", "-c", fmt.Sprintf("echo \"%s\" > %s", urlEntry.Text, UrlConfigFile)).Run()
                
                go func() {
                    term.Write([]byte("\x1b[33m[DL] Mendownload...\x1b[0m\n"))
                    
                    // Show masked URL in logs
                    term.Write([]byte(fmt.Sprintf("\x1b[90mDari: %s\x1b[0m\n", maskURL(urlEntry.Text))))
                    
                    if err := downloadGameConfig(urlEntry.Text, OnlineAccFile); err == nil {
                        term.Write([]byte("\x1b[32m[DL] Berhasil diunduh.\x1b[0m\n"))
                        processAccountFileLogic(w, overlay, term, OnlineAccFile, true)
                    } else {
                        term.Write([]byte("\x1b[31m[ERR] Gagal Download.\x1b[0m\n"))
                        removeFileRoot(OnlineAccFile)
                        
                        // Show error popup and retry
                        showDownloadErrorPopup(w, overlay, term)
                    }
                }()
            }
        },
        fyne.NewSize(350, 200))
}

func showDownloadErrorPopup(w fyne.Window, overlay *fyne.Container, term *Terminal) {
    content := container.NewVBox(
        canvas.NewText("Download Gagal!", theme.ErrorColor()),
        widget.NewLabel(""),
        widget.NewLabel("Kemungkinan penyebab:"),
        widget.NewLabel("• URL tidak valid"),
        widget.NewLabel("• Server offline"),
        widget.NewLabel("• Koneksi error"),
    )
    
    showGamePopup(w, overlay, "DOWNLOAD ERROR",
        content,
        "BATAL", nil,
        "COBA LAGI", func() {
            showURLInputPopup(w, overlay, term)
        },
        fyne.NewSize(350, 250))
}

func processAccountFileLogic(w fyne.Window, overlay *fyne.Container, term *Terminal, path string, isOnline bool) {
    ids, names, displays, err := parseAccountFile(path)
    if err != nil {
        term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] %s\x1b[0m\n", err.Error())))
        if isOnline {
            removeFileRoot(OnlineAccFile)
        }
        return
    }
    
    // Show account list popup
    showAccountListPopup(w, overlay, term, ids, names, displays, isOnline)
}

func showManualIDPopup(w fyne.Window, overlay *fyne.Container, term *Terminal) {
    entry := widget.NewEntry()
    entry.SetPlaceHolder("Masukkan Device ID...")
    
    content := container.NewVBox(
        widget.NewLabel("Input Device ID Manual:"),
        widget.NewSeparator(),
        entry,
    )
    
    showGamePopup(w, overlay, "INPUT MANUAL",
        content,
        "BATAL", nil,
        "TERAPKAN", func() {
            if len(entry.Text) > 5 {
                runMLBBTask(term, "Set Manual ID", func() {
                    term.Write([]byte(fmt.Sprintf("\x1b[36m[INFO] ID Baru: %s\x1b[0m\n", entry.Text)))
                    applyDeviceIDLogic(term, entry.Text, PackageNames[SelectedGameIdx], 
                        AppNames[SelectedGameIdx], "MANUAL")
                })
            }
        },
        fyne.NewSize(350, 200))
}

/* ==========================================
   SIDE MENU & UI
========================================== */
type EdgeTrigger struct { widget.BaseWidget; OnOpen func() }
func NewEdgeTrigger(onOpen func()) *EdgeTrigger { e := &EdgeTrigger{OnOpen: onOpen}; e.ExtendBaseWidget(e); return e }
func (e *EdgeTrigger) Dragged(event *fyne.DragEvent) { if event.Dragged.DX > 10 && e.OnOpen != nil { e.OnOpen() } }
func (e *EdgeTrigger) DragEnd() {}
func (e *EdgeTrigger) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent)) }

// HELPER OVERLAY
func showCustomOverlay(overlay *fyne.Container, title string, content fyne.CanvasObject, btn1Text string, act1 func(), btn2Text string, act2 func()) {
	overlay.Objects = nil
	lblTitle := createLabel(title, theme.ForegroundColor(), 18, true)
	
	var btnBox *fyne.Container
	if btn2Text != "" {
		b1 := widget.NewButton(btn1Text, func(){ overlay.Hide(); if act1 != nil { act1() } })
		b1.Importance = widget.DangerImportance
		b2 := widget.NewButton(btn2Text, func(){ overlay.Hide(); if act2 != nil { act2() } })
		b2.Importance = widget.HighImportance
		btnBox = container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(110,40), b1), widget.NewLabel(" "), container.NewGridWrap(fyne.NewSize(110,40), b2), layout.NewSpacer())
	} else {
		b1 := widget.NewButton(btn1Text, func(){ overlay.Hide(); if act1 != nil { act1() } })
		b1.Importance = widget.HighImportance
		btnBox = container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(110,40), b1), layout.NewSpacer())
	}

	cardContent := container.NewVBox(
		container.NewPadded(container.NewCenter(lblTitle)), container.NewPadded(content), widget.NewLabel(""), btnBox,
	)
	card := widget.NewCard("", "", container.NewPadded(cardContent))
	wrapper := container.NewCenter(container.NewGridWrap(fyne.NewSize(340, 600), container.NewPadded(card)))
	
	overlay.Objects = []fyne.CanvasObject{canvas.NewRectangle(color.RGBA{0,0,0,220}), wrapper}
	overlay.Show(); overlay.Refresh()
}

func makeSideMenu(w fyne.Window, term *Terminal, overlayContainer *fyne.Container, onClose func()) (*fyne.Container, func()) {
	dimmer := canvas.NewRectangle(color.RGBA{0, 0, 0, 150})
	btnDimmer := widget.NewButton("", onClose); btnDimmer.Importance = widget.LowImportance
	dimmerContainer := container.NewStack(dimmer, btnDimmer)
	bgMenu := canvas.NewRectangle(theme.BackgroundColor())
	
	// FIX: Lebar Menu
	spacerWidth := canvas.NewRectangle(color.Transparent)
	spacerWidth.SetMinSize(fyne.NewSize(310, 10))

	lblTitle := widget.NewLabelWithStyle("GAME TOOLS", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})

	selGame := widget.NewSelect(AppNames, func(s string) { for i, v := range AppNames { if v == s { SelectedGameIdx = i } } })
	selGame.SetSelected(AppNames[0])
	cardTarget := widget.NewCard("Target Game", "", container.NewPadded(selGame))

	// --- LOGIN AKUN ---
	btnLogin := widget.NewButtonWithIcon("Login Akun", theme.LoginIcon(), func() {
		onClose()
		
		btnOnline := widget.NewButton("ONLINE", nil)
		btnOffline := widget.NewButton("OFFLINE", nil)
		content := container.NewGridWithColumns(2, btnOffline, btnOnline)
		
		// Process offline account file
		processOfflineAccount := func() {
			overlayContainer.Hide()
			term.Write([]byte("\x1b[33m[INFO] Mode Offline\x1b[0m\n"))
			ids, names, displays, err := parseAccountFile(AccountFile)
			if err != nil {
				term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] %s\x1b[0m\n", err.Error())))
				return
			}
			showAccountListPopup(w, overlayContainer, term, ids, names, displays, false)
		}

		btnOnline.OnTapped = func() {
			overlayContainer.Hide()
			
			// First, try to use saved URL
			var defaultUrl string
			cmd := exec.Command("su", "-c", "cat "+UrlConfigFile)
			out, err := cmd.Output()
			if err == nil {
				defaultUrl = cleanString(string(out))
			}
			
			if defaultUrl != "" && strings.HasPrefix(defaultUrl, "http") {
				go func() {
					term.Write([]byte(fmt.Sprintf("\x1b[33m[DL] Download dari URL tersimpan...\x1b[0m\n")))
					
					if err := downloadGameConfig(defaultUrl, OnlineAccFile); err == nil {
						term.Write([]byte("\x1b[32m[DL] Sukses.\x1b[0m\n"))
						processAccountFileLogic(w, overlayContainer, term, OnlineAccFile, true)
					} else {
						term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] Gagal download dari URL tersimpan.\x1b[0m\n")))
						removeFileRoot(OnlineAccFile)
						
						// Show URL input popup on failure
						showURLInputPopup(w, overlayContainer, term)
					}
				}()
			} else {
				// Show URL input popup directly
				showURLInputPopup(w, overlayContainer, term)
			}
		}
		
		btnOffline.OnTapped = func() { 
			processOfflineAccount()
		}
		
		showGamePopup(w, overlayContainer, "SUMBER AKUN", 
			content,
			"BATAL", nil,
			"", nil,
			fyne.NewSize(300, 150))
	})
	
	// --- RESET ID (RANDOM / MANUAL) ---
	btnReset := widget.NewButtonWithIcon("Reset ID", theme.ViewRefreshIcon(), func() {
		onClose()
		
		content := container.NewVBox(
			widget.NewLabel("Pilih metode reset ID:"),
			widget.NewSeparator(),
		)
		
		showGamePopup(w, overlayContainer, "RESET ID",
			content,
			"MANUAL", func() {
				// Manual input
				showManualIDPopup(w, overlayContainer, term)
			},
			"RANDOM", func() {
				// Random ID
				runMLBBTask(term, "Reset ID Random", func() {
					newID := generateRandomID()
					term.Write([]byte(fmt.Sprintf("\x1b[36m[INFO] ID Baru: %s\x1b[0m\n", newID)))
					applyDeviceIDLogic(term, newID, PackageNames[SelectedGameIdx], 
						AppNames[SelectedGameIdx], "GUEST/NEW")
				})
			},
			fyne.NewSize(350, 180))
	})
	
	// --- SALIN ID ---
	btnCopy := widget.NewButtonWithIcon("Salin ID", theme.ContentCopyIcon(), func() {
		onClose()
		
		selSrc := widget.NewSelect(AppNames, nil)
		selSrc.PlaceHolder = "Pilih Sumber"
		
		content := container.NewVBox(
			widget.NewLabel("Salin ID Dari:"),
			widget.NewSeparator(),
			selSrc,
		)
		
		showGamePopup(w, overlayContainer, "SALIN ID",
			content,
			"BATAL", nil,
			"SALIN", func() {
				if selSrc.Selected != "" {
					srcIdx := 0
					for i, v := range AppNames {
						if v == selSrc.Selected {
							srcIdx = i
							break
						}
					}
					
					runMLBBTask(term, "Salin ID", func() {
						cmdStr := fmt.Sprintf("sed -n 's/.*<string name=\"JsonDeviceID\">\\([^<]*\\)<.*/\\1/p' "+
							"/data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml | head -n 1", 
							PackageNames[srcIdx], PackageNames[srcIdx])
						
						out, err := exec.Command("su", "-c", cmdStr).Output()
						srcID := cleanString(string(out))
						
						if err == nil && len(srcID) > 5 {
							term.Write([]byte(fmt.Sprintf("\x1b[36m[INFO] ID Salinan: %s\x1b[0m\n", srcID)))
							applyDeviceIDLogic(term, srcID, PackageNames[SelectedGameIdx], 
								AppNames[SelectedGameIdx], "HASIL COPY")
						} else {
							term.Write([]byte("\x1b[31m[ERR] Gagal/Kosong.\x1b[0m\n"))
						}
					})
				}
			},
			fyne.NewSize(350, 180))
	})

	cardAccount := widget.NewCard("Akun Manager", "", container.NewPadded(container.NewGridWithColumns(1, btnLogin, btnReset, btnCopy)))

	// TOMBOL KELUAR (FIX POSISI)
	btnExit := widget.NewButtonWithIcon("Keluar", theme.LogoutIcon(), func() { os.Exit(0) }); btnExit.Importance = widget.DangerImportance

	// CONTENT MENU TENGAH (SCROLLABLE)
	menuContent := container.NewVBox(
		container.NewPadded(lblTitle), widget.NewSeparator(),
		cardTarget, cardAccount, 
		layout.NewSpacer(), 
		widget.NewSeparator(), 
	)
	
	// FIX LAYOUT: Menggunakan Border. Bottom=btnExit memaksa tombol keluar SELALU di bawah.
	finalMenuLayout := container.NewBorder(nil, container.NewPadded(btnExit), nil, nil, container.NewVScroll(menuContent))
	
	// PANEL UTAMA
	panel := container.NewStack(bgMenu, spacerWidth, container.NewPadded(finalMenuLayout))
	
	// SLIDE CONTAINER
	slideContainer := container.NewBorder(nil, nil, panel, nil)
	
	finalMenu := container.NewStack(dimmerContainer, slideContainer); finalMenu.Hide()

	toggle := func() { if finalMenu.Visible() { finalMenu.Hide() } else { finalMenu.Show(); finalMenu.Refresh() } }
	return finalMenu, toggle
}

/* ===============================
              MAIN UI
================================ */
func main() {
	removeFileRoot(OnlineAccFile)

	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Simple Exec by TANGSAN")
	w.Resize(fyne.NewSize(400, 700))
	w.SetMaster()

	term := NewTerminal()
	
	go func() {
		time.Sleep(1 * time.Second)
		if !CheckRoot() { /* Optional Auto Request */ }
	}()

	if !CheckRoot() { currentDir = "/sdcard" }

	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	successGreen := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	failRed := color.RGBA{R: 255, G: 50, B: 50, A: 255}
	silverColor := color.Gray{Y: 180}

	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")
	status := canvas.NewText("System: Ready", silverColor)
	status.TextSize = 12; status.Alignment = fyne.TextAlignCenter

	lblKernelTitle := createLabel("KERNEL", brightYellow, 10, true)
	lblKernelValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSELinuxTitle := createLabel("SELINUX", brightYellow, 10, true)
	lblSELinuxValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSystemTitle := createLabel("ROOT", brightYellow, 10, true)
	lblSystemValue := createLabel("...", color.Gray{Y: 150}, 11, true)

	go func() {
		time.Sleep(1 * time.Second)
		for {
			func() {
				defer func() { if r := recover(); r != nil {} }()
				if CheckRoot() {
					lblSystemValue.Text = "GRANTED"; lblSystemValue.Color = successGreen
				} else {
					lblSystemValue.Text = "DENIED"; lblSystemValue.Color = failRed
				}
				lblSystemValue.Refresh()
				
				if CheckKernelDriver() {
					lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				} else {
					lblKernelValue.Text = "MISSING"; lblKernelValue.Color = failRed
				}
				lblKernelValue.Refresh()
				
				se := CheckSELinux()
				lblSELinuxValue.Text = strings.ToUpper(se)
				if se == "Enforcing" { lblSELinuxValue.Color = successGreen } else if se == "Permissive" { lblSELinuxValue.Color = failRed } else { lblSELinuxValue.Color = color.Gray{Y: 150} }
				lblSELinuxValue.Refresh()
			}()
			time.Sleep(3 * time.Second)
		}
	}()

	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		status.Text = "Status: Processing..."
		status.Refresh()

		if !isScript {
			displayDir := currentDir
			if len(displayDir) > 25 { displayDir = "..." + displayDir[len(displayDir)-20:] }
			term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0m%s\n", displayDir, cmdText)))
		}

		go func() {
			var cmd *exec.Cmd
			isRoot := CheckRoot()

			if isScript {
				if isRoot {
					target := "/data/local/tmp/temp_exec"
					exec.Command("su", "-c", "rm -f "+target).Run()
					exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", scriptPath, target, target)).Run()
					if !isBinary { cmd = exec.Command("su", "-c", "sh "+target) } else { cmd = exec.Command("su", "-c", target) }
				} else {
					if !isBinary { cmd = exec.Command("sh", scriptPath) } else { cmd = exec.Command(scriptPath) }
				}
			} else {
				if isRoot {
					cmd = exec.Command("su", "-c", fmt.Sprintf("cd \"%s\" && %s", currentDir, cmdText))
				} else {
					runCmd := cmdText
					if strings.HasPrefix(cmdText, "ls") { if !strings.Contains(cmdText, "-a") { runCmd = strings.Replace(cmdText, "ls", "ls -a", 1) } }
					if strings.HasPrefix(cmdText, "./") {
						fileName := strings.TrimPrefix(cmdText, "./")
						fullPath := filepath.Join(currentDir, fileName)
						if strings.HasSuffix(fileName, ".sh") {
							runCmd = fmt.Sprintf("sh \"%s\"", fullPath)
						} else {
							tmpBin := filepath.Join(os.TempDir(), fileName)
							if err := copyFile(fullPath, tmpBin); err == nil { os.Chmod(tmpBin, 0755); runCmd = tmpBin } else { runCmd = fullPath }
						}
					}
					cmd = exec.Command("sh", "-c", runCmd)
					cmd.Dir = currentDir
				}
			}

			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			stderr, _ := cmd.StderrPipe()
			cmdMutex.Lock(); activeStdin = stdin; cmdMutex.Unlock()

			if err := cmd.Start(); err != nil {
				term.Write([]byte(fmt.Sprintf("\x1b[31mError: %s\x1b[0m\n", err.Error())))
				cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
				return
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); io.Copy(term, stdout) }()
			go func() { defer wg.Done(); io.Copy(term, stderr) }()
			wg.Wait(); cmd.Wait()
			cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
			status.Text = "Status: Idle"; status.Refresh()
			if isScript && isRoot { exec.Command("su", "-c", "rm -f /data/local/tmp/temp_exec").Run() }
		}()
	}

	send := func() {
		text := input.Text
		input.SetText("")
		cmdMutex.Lock()
		if activeStdin != nil { io.WriteString(activeStdin, text+"\n"); term.Write([]byte(text + "\n")); cmdMutex.Unlock(); return }
		cmdMutex.Unlock()
		if strings.TrimSpace(text) == "" { return }
		
		if strings.HasPrefix(text, "cd") {
			parts := strings.Fields(text)
			newPath := currentDir
			if len(parts) == 1 { if CheckRoot() { newPath = "/data/local/tmp" } else { h, _ := os.UserHomeDir(); newPath = h } } else { arg := parts[1]; if filepath.IsAbs(arg) { newPath = arg } else { newPath = filepath.Join(currentDir, arg) } }
			newPath = filepath.Clean(newPath)
			exist := false
			if CheckRoot() { if exec.Command("su", "-c", "[ -d \""+newPath+"\" ]").Run() == nil { exist = true } } else { if info, err := os.Stat(newPath); err == nil && info.IsDir() { exist = true } }
			if exist { currentDir = newPath; displayDir := currentDir; if len(displayDir) > 25 { displayDir = "..." + displayDir[len(displayDir)-20:] }; term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0mcd %s\n", displayDir, parts[1]))) } else { term.Write([]byte(fmt.Sprintf("\x1b[31mcd: %s: No such directory\x1b[0m\n", parts[1]))) }
			return
		}
		executeTask(text, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close(); term.Clear()
		data, err := io.ReadAll(reader)
		if err != nil { term.Write([]byte("\x1b[31m[ERR] Read Failed\x1b[0m\n")); return }
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		tmpFile, err := os.CreateTemp("", "exec_tmp")
		if err != nil { term.Write([]byte("\x1b[31m[ERR] Write Failed\x1b[0m\n")); return }
		tmpPath := tmpFile.Name(); tmpFile.Write(data); tmpFile.Close(); os.Chmod(tmpPath, 0755)
		executeTask("", true, tmpPath, isBinary)
	}

	var overlayContainer *fyne.Container
	
	showModal := func(title, msg, confirm string, action func(), isErr bool, isForce bool) {
		w.Canvas().Refresh(w.Content())
		
		cancelLabel := "BATAL"
		cancelFunc := func() { overlayContainer.Hide() }
		
		if isForce {
			cancelLabel = "KELUAR"
			cancelFunc = func() { os.Exit(0) }
		}
		
		btnCancel := widget.NewButton(cancelLabel, cancelFunc)
		btnCancel.Importance = widget.DangerImportance
		
		btnOk := widget.NewButton(confirm, func() {
			if !isForce { overlayContainer.Hide() }
			if action != nil { action() }
		})
		
		if confirm == "COBA LAGI" {
			btnOk.Importance = widget.HighImportance
		} else {
			if isErr { 
				btnOk.Importance = widget.DangerImportance 
			} else { 
				btnOk.Importance = widget.HighImportance 
			}
		}
		
		btnBox := container.NewHBox(
			layout.NewSpacer(), 
			container.NewGridWrap(fyne.NewSize(110,40), btnCancel), 
			widget.NewLabel("   "), 
			container.NewGridWrap(fyne.NewSize(110,40), btnOk), 
			layout.NewSpacer(),
		)
		
		lblTitle := createLabel(title, theme.ForegroundColor(), 18, true)
		if isErr { lblTitle.Color = theme.ErrorColor() }
		
		lblMsg := widget.NewLabel(msg)
		lblMsg.Alignment = fyne.TextAlignCenter 
		lblMsg.Wrapping = fyne.TextWrapWord
		
		content := container.NewVBox(
			container.NewPadded(container.NewCenter(lblTitle)), 
			container.NewPadded(lblMsg), 
			widget.NewLabel(""), 
			btnBox,
		)
		
		card := widget.NewCard("", "", container.NewPadded(content))
		wrapper := container.NewCenter(container.NewGridWrap(fyne.NewSize(300, 220), container.NewPadded(card)))
		
		overlayContainer.Objects = []fyne.CanvasObject{canvas.NewRectangle(color.RGBA{0,0,0,220}), wrapper}
		overlayContainer.Show(); overlayContainer.Refresh()
	}

	autoInstallKernel := func() {
		term.Clear()
		status.Text = "Sistem: Memproses..."
		status.Refresh()

		go func() {
			term.Write([]byte("\x1b[36m╔════ PENGINSTAL DRIVER ════╗\x1b[0m\n"))

			// 1. Cek Versi Kernel
			out, _ := exec.Command("uname", "-r").Output()
			fullVer := strings.TrimSpace(string(out))
			targetVer := strings.Split(fullVer, "-")[0]
			
			term.Write([]byte(fmt.Sprintf("Kernel: \x1b[33m%s\x1b[0m\n", fullVer)))

			// Path tujuan
			targetKoPath := "/data/local/tmp/module_inject.ko"
			
			// DEFER cleanup
			defer func() {
				exec.Command("su", "-c", "rm -f "+targetKoPath).Run()
			}()

			// 2. Baca ZIP Embed
			term.Write([]byte("\x1b[97m[*] Membaca file driver internal...\x1b[0m\n"))
			zipReader, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err != nil {
				term.Write([]byte("\x1b[31m[ERR] File Zip Rusak/Corrupt\x1b[0m\n")); return
			}

			// 3. Cari File .ko
			var fileToExtract *zip.File
			for _, f := range zipReader.File {
				if strings.HasSuffix(f.Name, ".ko") && strings.Contains(f.Name, targetVer) {
					fileToExtract = f; break
				}
			}
			
			// Fallback
			if fileToExtract == nil {
				for _, f := range zipReader.File {
					if strings.HasSuffix(f.Name, ".ko") { fileToExtract = f; break }
				}
			}

			if fileToExtract == nil {
				term.Write([]byte("\x1b[31m[GAGAL] Modul .ko tidak ditemukan di dalam Zip!\x1b[0m\n"))
				status.Text = "File Hilang"; status.Refresh()
				return
			}

			// 4. Ekstrak
			term.Write([]byte(fmt.Sprintf("\x1b[32m[+] Menggunakan File: %s\x1b[0m\n", fileToExtract.Name)))
			rc, _ := fileToExtract.Open()
			buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
			
			userTmp := filepath.Join(os.TempDir(), "temp_mod.ko")
			os.WriteFile(userTmp, buf.Bytes(), 0644)
			
			// Pindah ke root path
			exec.Command("su", "-c", fmt.Sprintf("cp %s %s", userTmp, targetKoPath)).Run()
			exec.Command("su", "-c", "chmod 777 "+targetKoPath).Run()
			os.Remove(userTmp) 

			// 5. Install (Insmod)
			term.Write([]byte("\x1b[36m[*] Memasang Modul (Inject)...\x1b[0m\n"))
			cmdInsmod := exec.Command("su", "-c", "insmod "+targetKoPath)
			output, err := cmdInsmod.CombinedOutput()
			outputStr := string(output)

			// 6. Cek Hasil
			if err == nil {
				term.Write([]byte("\x1b[92m[SUKSES] Driver Berhasil Di install\x1b[0m\n"))
				lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				status.Text = "Berhasil Install"
			} else if strings.Contains(outputStr, "File exists") {
				term.Write([]byte("\x1b[33m[INFO] Driver Sudah Ada Ketik insmod untuk cek lebih lanjut\x1b[0m\n"))
				lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				status.Text = "Sudah Aktif"
			} else {
				term.Write([]byte("\x1b[31m[GAGAL] Gagal install\x1b[0m\n"))
				term.Write([]byte("\x1b[31m" + outputStr + "\x1b[0m\n"))
				lblKernelValue.Text = "ERROR"; lblKernelValue.Color = failRed
				status.Text = "Gagal Install"
			}
			
			lblKernelValue.Refresh()
			status.Refresh()
		}()
	}

	var checkUpdate func()
	checkUpdate = func() {
		overlayContainer.Hide()
		time.Sleep(500 * time.Millisecond) 
		if strings.Contains(ConfigURL, "GANTI") { term.Write([]byte("\n\x1b[33m[WARN] ConfigURL!\x1b[0m\n")); return }
		term.Write([]byte("\n\x1b[90m[*] Checking updates...\x1b[0m\n"))
		
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix()))
		
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body); resp.Body.Close()
			if dec, err := decryptConfig(string(bytes.TrimSpace(body))); err == nil {
				var cfg OnlineConfig
				if json.Unmarshal(dec, &cfg) == nil {
					if cfg.Version != "" && cfg.Version != AppVersion {
						showCustomOverlay(overlayContainer, "UPDATE", widget.NewLabel(cfg.Message), "BATAL", nil, "UPDATE", func(){ 
							if u, e := url.Parse(cfg.Link); e == nil { app.New().OpenURL(u) } 
						})
					} else {
						term.Write([]byte("\x1b[32m[V] System Updated.\x1b[0m\n"))
					}
				}
			}
		} else {
			showModal("ERROR", "Gagal terhubung ke server.\nPeriksa koneksi internet.", "COBA LAGI", func() {
				go checkUpdate()
			}, true, true)
		}
	}

	go func() {
		time.Sleep(1500 * time.Millisecond)
		checkUpdate()
	}()

	titleText := createLabel("SIMPLE EXECUTOR", color.White, 16, true)
	
	infoGrid := container.NewGridWithColumns(3, 
		container.NewVBox(lblKernelTitle, lblKernelValue), 
		container.NewVBox(lblSELinuxTitle, lblSELinuxValue), 
		container.NewVBox(lblSystemTitle, lblSystemValue),
	)

	btnInj := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() { 
		showModal("INJECT", "Mulai Inject Driver?", "MULAI", func(){ autoInstallKernel() }, false, false) 
	})
	btnInj.Importance = widget.HighImportance

	btnSel := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() { 
		go func() { 
			if CheckSELinux() == "Enforcing" { 
				exec.Command("su","-c","setenforce 0").Run() 
			} else { 
				exec.Command("su","-c","setenforce 1").Run() 
			}
			time.Sleep(100 * time.Millisecond)
			se := CheckSELinux()
			lblSELinuxValue.Text = strings.ToUpper(se)
			if se == "Enforcing" { 
				lblSELinuxValue.Color = successGreen 
			} else if se == "Permissive" { 
				lblSELinuxValue.Color = failRed 
			} else { 
				lblSELinuxValue.Color = color.Gray{Y: 150} 
			}
			lblSELinuxValue.Refresh()
		}() 
	})
	btnSel.Importance = widget.HighImportance

	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { 
		term.Clear() 
	})
	btnClr.Importance = widget.DangerImportance
	
	headerContent := container.NewVBox(
		container.NewPadded(titleText), 
		container.NewPadded(infoGrid), 
		container.NewPadded(container.NewGridWithColumns(3, btnInj, btnSel, btnClr)), 
		container.NewPadded(status), 
		widget.NewSeparator(),
	)
	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), headerContent)

	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	cpyLbl := createLabel("Code by TANGSAN", silverColor, 10, false)
	bottom := container.NewVBox(container.NewPadded(cpyLbl), container.NewPadded(container.NewBorder(nil, nil, nil, sendBtn, input)))
	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bg.FillMode = canvas.ImageFillStretch
	termBox := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll)
	
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng})
	fdImg.FillMode = canvas.ImageFillContain
	
	fdBtn := widget.NewButton("", func() { 
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { 
			if r != nil { runFile(r) } 
		}, w).Show() 
	})
	fdBtn.Importance = widget.LowImportance
	
	fabWrapper := container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn))
	fab := container.NewVBox(
		layout.NewSpacer(), 
		container.NewPadded(container.NewHBox(layout.NewSpacer(), fabWrapper)), 
		widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "),
	)

	// --- INIT SIDE MENU & GESTURE (TAMBAHAN FINAL) ---
	var toggleMenu func() 
	
	// Init Overlay first
	overlayContainer = container.NewStack()
	overlayContainer.Hide()

	sideMenuContainer, toggleFunc := makeSideMenu(w, term, overlayContainer, func() { toggleMenu() })
	toggleMenu = toggleFunc

	// UPDATE SENSITIVITAS SLIDE MENU (50 Width)
	edgeTrigger := NewEdgeTrigger(func() {
		if !sideMenuContainer.Visible() { toggleMenu() }
	})
	triggerZone := container.NewHBox(container.NewGridWrap(fyne.NewSize(60, 1000), edgeTrigger), layout.NewSpacer())

	w.SetContent(container.NewStack(
		container.NewBorder(header, bottom, nil, nil, termBox), // Main UI
		triggerZone,       // Layer 2
		fab,               // Layer 3
		sideMenuContainer, // Layer 4
		overlayContainer,  // Layer 5
	))

	w.ShowAndRun()
}

