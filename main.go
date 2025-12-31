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
   CONFIG & UPDATE SYSTEM (KODE ASLI)
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
   SECURITY LOGIC (KODE ASLI)
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
   TERMINAL LOGIC (KODE ASLI)
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
	case "90": return color.Gray{Y: 100} // ABU-ABU (SESUAI PERMINTAAN)
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
   SYSTEM HELPERS (KODE ASLI)
================================ */
func CheckRoot() bool {
	cmd := exec.Command("su", "-c", "id -u")
	out, err := cmd.Output()
	if err != nil { return false }
	return strings.TrimSpace(string(out)) == "0"
}

func CheckKernelDriver() bool {
	cmd := exec.Command("su", "-c", "grep -q 'read_physical_address' /proc/kallsyms")
	return cmd.Run() == nil
}

func CheckSELinux() string {
	cmd := exec.Command("su", "-c", "getenforce")
	out, err := cmd.Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
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

func createLabel(text string, color color.Color, size float32, bold bool) *canvas.Text {
	lbl := canvas.NewText(text, color)
	lbl.TextSize = size; lbl.Alignment = fyne.TextAlignCenter
	if bold { lbl.TextStyle = fyne.TextStyle{Bold: true} }
	return lbl
}

/* ===============================
   LOGIKA MLBB & HELPER (ROOT BYPASS)
================================ */
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

// Helper: Download via CURL (Root) - Mengatasi Permission Denied
func downloadFileRoot(url string, filepath string) error {
	exec.Command("su", "-c", "rm -f "+filepath).Run()
	cmd := exec.Command("su", "-c", fmt.Sprintf("curl -k -L -f --connect-timeout 20 -o %s %s", filepath, url))
	return cmd.Run()
}

// Helper: Baca File via CAT (Root) - Mengatasi Permission Denied
func readFileRoot(path string) (string, error) {
	cmd := exec.Command("su", "-c", "cat \""+path+"\"")
	out, err := cmd.Output()
	if err != nil { return "", err }
	return string(out), nil
}

// Helper: Parsing dengan logic Root
func parseAccountFile(path string) ([]string, []string, []string, error) {
	var content string
	
	// Coba baca normal dulu
	b, err := os.ReadFile(path)
	if err == nil {
		content = string(b)
	} else {
		// Fallback root
		c, err2 := readFileRoot(path)
		if err2 != nil { return nil, nil, nil, fmt.Errorf("gagal baca file (Root): %v", err2) }
		content = c
	}

	var ids, names, displays []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	
	for scanner.Scan() {
		line := cleanString(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") { continue }
		
		// Format: DEVICEID<spasi>NAMA
		parts := strings.Fields(line)
		if len(parts) >= 1 {
			id := parts[0]
			name := "No Name"
			if len(parts) > 1 { name = strings.Join(parts[1:], " ") }
			
			ids = append(ids, id)
			names = append(names, name)
			displays = append(displays, name) // HANYA TAMPILKAN NAMA (SESUAI REQUEST)
		}
	}
	
	if len(ids) == 0 { return nil, nil, nil, errors.New("File kosong atau format salah") }
	return ids, names, displays, nil
}

func applyDeviceIDLogic(term *Terminal, targetID, targetPkg, targetAppName, customAccName string) {
	targetID = cleanString(targetID)
	targetFile := fmt.Sprintf("/data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml", targetPkg, targetPkg)

	// LOG: Package Name
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

	for _, cmd := range cmds {
		exec.Command("su", "-c", cmd).Run()
	}
	term.Write([]byte("\x1b[32m[SUKSES] ID Berhasil diterapkan.\x1b[0m\n"))
}

/* ==========================================
   SIDE MENU & UI
========================================== */
type EdgeTrigger struct {
	widget.BaseWidget
	OnOpen func()
}
func NewEdgeTrigger(onOpen func()) *EdgeTrigger {
	e := &EdgeTrigger{OnOpen: onOpen}
	e.ExtendBaseWidget(e)
	return e
}
func (e *EdgeTrigger) Dragged(event *fyne.DragEvent) {
	if event.Dragged.DX > 10 { if e.OnOpen != nil { e.OnOpen() } }
}
func (e *EdgeTrigger) DragEnd() {}
func (e *EdgeTrigger) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

// HELPER POPUP CARD (Overlay Manual untuk mencegah Force Close)
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
		container.NewPadded(container.NewCenter(lblTitle)), 
		container.NewPadded(content), 
		widget.NewLabel(""), 
		btnBox,
	)
	card := widget.NewCard("", "", container.NewPadded(cardContent))
	// UKURAN POPUP LEBIH TINGGI AGAR LIST MUAT
	wrapper := container.NewCenter(container.NewGridWrap(fyne.NewSize(340, 600), container.NewPadded(card)))
	
	overlay.Objects = []fyne.CanvasObject{canvas.NewRectangle(color.RGBA{0,0,0,220}), wrapper}
	overlay.Show(); overlay.Refresh()
}

func makeSideMenu(w fyne.Window, term *Terminal, overlayContainer *fyne.Container, onClose func()) (*fyne.Container, func()) {
	dimmer := canvas.NewRectangle(color.RGBA{0, 0, 0, 150})
	btnDimmer := widget.NewButton("", onClose)
	btnDimmer.Importance = widget.LowImportance
	dimmerContainer := container.NewStack(dimmer, btnDimmer)
	bgMenu := canvas.NewRectangle(theme.BackgroundColor())

	lblTitle := widget.NewLabelWithStyle("GAME TOOLS", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})

	selGame := widget.NewSelect(AppNames, func(s string) {
		for i, v := range AppNames { if v == s { SelectedGameIdx = i } }
	})
	selGame.SetSelected(AppNames[0])
	cardTarget := widget.NewCard("Target Game", "", container.NewPadded(selGame))

	// --- LOGIN AKUN ---
	btnLogin := widget.NewButtonWithIcon("Login Akun", theme.LoginIcon(), func() {
		onClose()
		btnOnline := widget.NewButton("ONLINE", nil)
		btnOffline := widget.NewButton("OFFLINE", nil)
		content := container.NewGridWithColumns(2, btnOffline, btnOnline)
		
		processAccountFile := func(path string, isOnline bool) {
			ids, rNames, dList, err := parseAccountFile(path)
			if err != nil { 
				term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] %s\x1b[0m\n", err.Error())))
				if isOnline { os.Remove(OnlineAccFile) } 
				return 
			}

			selectedIndex := -1
			
			// LIST AKUN (FIX: Ceklis + Nama)
			listWidget := widget.NewList(
				func() int { return len(dList) },
				func() fyne.CanvasObject { 
					icon := widget.NewIcon(theme.ConfirmIcon()) 
					icon.Hide()
					lbl := widget.NewLabel("Template Name")
					lbl.TextStyle = fyne.TextStyle{Bold:true}
					return container.NewHBox(icon, lbl)
				},
				func(i int, o fyne.CanvasObject) { 
					box := o.(*fyne.Container)
					icon := box.Objects[0].(*widget.Icon)
					lbl := box.Objects[1].(*widget.Label)
					lbl.SetText(dList[i])
					if i==selectedIndex { icon.Show(); lbl.TextStyle.Italic=true } else { icon.Hide(); lbl.TextStyle.Italic=false }
				},
			)
			
			// WRAPPER LIST (Mencegah Force Close)
			listContainer := container.NewGridWrap(fyne.NewSize(300, 400), listWidget)

			showCustomOverlay(overlayContainer, "DAFTAR AKUN", listContainer, "BATAL", func() {
				if isOnline { os.Remove(OnlineAccFile) }
			}, "PILIH", func() {
				if selectedIndex >= 0 {
					runMLBBTask(term, "Login: "+rNames[selectedIndex], func() {
						applyDeviceIDLogic(term, ids[selectedIndex], PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], rNames[selectedIndex])
						exec.Command("su", "-c", fmt.Sprintf("am start -n %s/com.moba.unityplugin.MobaGameUnityActivity", PackageNames[SelectedGameIdx])).Run()
						// HAPUS SETELAH SUKSES
						if isOnline { os.Remove(OnlineAccFile) }
					})
				} else {
					// HAPUS JIKA TIDAK JADI
					if isOnline { os.Remove(OnlineAccFile) }
				}
			})
			
			listWidget.OnSelected = func(id int) { selectedIndex = id; listWidget.Refresh() }
		}

		btnOnline.OnTapped = func() {
			overlayContainer.Hide()
			// Auto Deteksi Config via Root
			defaultUrl := ""
			content, err := readFileRoot(UrlConfigFile)
			if err == nil { defaultUrl = cleanString(content) }

			if defaultUrl != "" {
				go func() {
					term.Write([]byte(fmt.Sprintf("\x1b[33m[DL] URL tersimpan: %s\x1b[0m\n", defaultUrl)))
					// DOWNLOAD VIA ROOT
					if err := downloadFileRoot(defaultUrl, OnlineAccFile); err == nil {
						term.Write([]byte("\x1b[32m[DL] Sukses.\x1b[0m\n"))
						dialog.NewCustom("Loading", "Hide", widget.NewLabel(""), w).Hide() 
						processAccountFile(OnlineAccFile, true)
					} else {
						term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] Gagal Download.\x1b[0m\n")))
						os.Remove(OnlineAccFile)
					}
				}()
			} else {
				entryUrl := widget.NewEntry(); entryUrl.SetPlaceHolder("https://...")
				showCustomOverlay(overlayContainer, "INPUT URL", entryUrl, "BATAL", nil, "DOWNLOAD", func() {
					if entryUrl.Text != "" {
						os.WriteFile(UrlConfigFile, []byte(entryUrl.Text), 0644)
						go func() {
							term.Write([]byte("\x1b[33m[DL] Mendownload...\x1b[0m\n"))
							// DOWNLOAD VIA ROOT
							if err := downloadFileRoot(entryUrl.Text, OnlineAccFile); err == nil {
								processAccountFile(OnlineAccFile, true)
							} else {
								term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] Gagal Download.\x1b[0m\n")))
								os.Remove(OnlineAccFile)
							}
						}()
					}
				})
			}
		}

		btnOffline.OnTapped = func() {
			overlayContainer.Hide()
			term.Write([]byte("\x1b[33m[INFO] Mode Offline\x1b[0m\n"))
			processAccountFile(AccountFile, false)
		}
		showCustomOverlay(overlayContainer, "SUMBER AKUN", content, "BATAL", nil, "", nil)
	})
	
	btnReset := widget.NewButtonWithIcon("Reset ID", theme.ViewRefreshIcon(), func() {
		onClose()
		showCustomOverlay(overlayContainer, "RESET ID", widget.NewLabel("Ganti ID menjadi Random?"), "TIDAK", nil, "YA", func() {
			runMLBBTask(term, "Reset ID Random", func() {
				applyDeviceIDLogic(term, generateRandomID(), PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], "GUEST/NEW")
			})
		})
	})
	
	btnCopy := widget.NewButtonWithIcon("Salin ID", theme.ContentCopyIcon(), func() {
		onClose()
		selSrc := widget.NewSelect(AppNames, nil); selSrc.PlaceHolder = "Pilih Sumber"
		content := container.NewVBox(widget.NewLabel("Salin ID Dari:"), selSrc)
		showCustomOverlay(overlayContainer, "SALIN ID", content, "BATAL", nil, "SALIN", func() {
			if selSrc.Selected != "" {
				srcIdx := 0; for i, v := range AppNames { if v == selSrc.Selected { srcIdx = i } }
				runMLBBTask(term, "Salin ID", func() {
					cmdStr := fmt.Sprintf("sed -n 's/.*<string name=\"JsonDeviceID\">\\([^<]*\\)<.*/\\1/p' /data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml | head -n 1", PackageNames[srcIdx], PackageNames[srcIdx])
					out, err := exec.Command("su", "-c", cmdStr).Output(); srcID := cleanString(string(out))
					if err == nil && len(srcID) > 5 {
						applyDeviceIDLogic(term, srcID, PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], "HASIL COPY")
					} else { term.Write([]byte("\x1b[31m[ERR] Gagal/Kosong.\x1b[0m\n")) }
				})
			}
		})
	})

	cardAccount := widget.NewCard("Akun Manager", "", container.NewPadded(container.NewGridWithColumns(1, btnLogin, btnReset, btnCopy)))

	btnExit := widget.NewButtonWithIcon("Keluar", theme.LogoutIcon(), func() { os.Exit(0) }); btnExit.Importance = widget.DangerImportance

	scrollContent := container.NewVBox(
		container.NewPadded(lblTitle), widget.NewSeparator(),
		cardTarget, cardAccount, 
		layout.NewSpacer(), 
		widget.NewSeparator(), btnExit,
	)
	
	// SIZE: Lebar 310, Tinggi Full
	panel := container.NewStack(bgMenu, container.NewPadded(container.NewVScroll(scrollContent)))
	slideContainer := container.NewHBox(container.NewGridWrap(fyne.NewSize(310, 2000), panel))
	finalMenu := container.NewStack(dimmerContainer, slideContainer); finalMenu.Hide()

	toggle := func() { if finalMenu.Visible() { finalMenu.Hide() } else { finalMenu.Show(); finalMenu.Refresh() } }
	return finalMenu, toggle
}

/* ===============================
              MAIN UI
================================ */
func main() {
	// 1. Cleanup awal saat aplikasi dibuka
	os.Remove(OnlineAccFile)

	a := app.New(); a.Settings().SetTheme(theme.DarkTheme())
	w := a.NewWindow("Simple Exec by TANGSAN"); w.Resize(fyne.NewSize(400, 700)); w.SetMaster()
	term := NewTerminal()
	
	go func() { time.Sleep(1*time.Second); if !CheckRoot() {} }()
	if !CheckRoot() { currentDir = "/sdcard" }

	brightYellow := color.RGBA{255, 255, 0, 255}; successGreen := color.RGBA{0, 255, 0, 255}; failRed := color.RGBA{255, 50, 50, 255}
	input := widget.NewEntry(); input.SetPlaceHolder("Terminal Command...")
	status := canvas.NewText("System: Ready", color.Gray{Y: 180}); status.TextSize = 12; status.Alignment = fyne.TextAlignCenter
	
	lblK := createLabel("KERNEL", brightYellow, 10, true); valK := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblS := createLabel("SELINUX", brightYellow, 10, true); valS := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblR := createLabel("ROOT", brightYellow, 10, true); valR := createLabel("...", color.Gray{Y: 150}, 11, true)

	go func() {
		for {
			func() {
				defer func() { recover() }()
				if CheckRoot() { valR.Text="GRANTED"; valR.Color=successGreen } else { valR.Text="DENIED"; valR.Color=failRed }
				if CheckKernelDriver() { valK.Text="ACTIVE"; valK.Color=successGreen } else { valK.Text="MISSING"; valK.Color=failRed }
				se := CheckSELinux(); valS.Text = strings.ToUpper(se)
				if se == "Enforcing" { valS.Color=successGreen } else { valS.Color=failRed }
				valR.Refresh(); valK.Refresh(); valS.Refresh()
			}()
			time.Sleep(3 * time.Second)
		}
	}()

	execute := func(cmdTxt string, isScript bool, scriptPath string, isBin bool) {
		status.Text = "Processing..."; status.Refresh()
		if !isScript { term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0m%s\n", currentDir, cmdTxt))) }
		go func() {
			var cmd *exec.Cmd
			if isScript {
				if CheckRoot() {
					t := "/data/local/tmp/temp_exec"; exec.Command("su", "-c", "rm -f "+t).Run()
					exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", scriptPath, t, t)).Run()
					if !isBin { cmd = exec.Command("su", "-c", "sh "+t) } else { cmd = exec.Command("su", "-c", t) }
				} else {
					if !isBin { cmd = exec.Command("sh", scriptPath) } else { cmd = exec.Command(scriptPath) }
				}
			} else {
				if CheckRoot() { cmd = exec.Command("su", "-c", fmt.Sprintf("cd \"%s\" && %s", currentDir, cmdTxt)) } else {
					run := cmdTxt; if strings.HasPrefix(cmdTxt, "ls") && !strings.Contains(cmdTxt, "-a") { run = strings.Replace(cmdTxt, "ls", "ls -a", 1) }
					cmd = exec.Command("sh", "-c", run); cmd.Dir = currentDir
				}
			}
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			stdin, _ := cmd.StdinPipe(); stdout, _ := cmd.StdoutPipe(); stderr, _ := cmd.StderrPipe()
			cmdMutex.Lock(); activeStdin = stdin; cmdMutex.Unlock()
			if err := cmd.Start(); err != nil { term.Write([]byte(fmt.Sprintf("\x1b[31mErr: %s\x1b[0m\n", err))); cmdMutex.Lock(); activeStdin=nil; cmdMutex.Unlock(); return }
			var wg sync.WaitGroup; wg.Add(2)
			go func() { defer wg.Done(); io.Copy(term, stdout) }()
			go func() { defer wg.Done(); io.Copy(term, stderr) }()
			wg.Wait(); cmd.Wait()
			cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock(); status.Text = "Idle"; status.Refresh()
		}()
	}

	send := func() {
		txt := input.Text; input.SetText(""); cmdMutex.Lock()
		if activeStdin != nil { io.WriteString(activeStdin, txt+"\n"); term.Write([]byte(txt+"\n")); cmdMutex.Unlock(); return }
		cmdMutex.Unlock()
		if strings.TrimSpace(txt) == "" { return }
		if strings.HasPrefix(txt, "cd") {
			parts := strings.Fields(txt); newP := currentDir
			if len(parts) > 1 { newP = filepath.Join(currentDir, parts[1]) }
			currentDir = newP; term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0mcd %s\n", currentDir, parts[1]))); return
		}
		execute(txt, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	// Overlay Container (DEFINISI AWAL)
	var overlayContainer *fyne.Container = container.NewStack(); overlayContainer.Hide()

	doInject := func() {
		term.Clear(); term.Write([]byte("\x1b[36m[*] Injecting Driver...\x1b[0m\n"))
		go func() {
			out, _ := exec.Command("uname", "-r").Output(); ver := strings.TrimSpace(string(out)); tVer := strings.Split(ver, "-")[0]
			term.Write([]byte(fmt.Sprintf("Kernel: %s\n", ver)))
			tKo := "/data/local/tmp/module_inject.ko"; exec.Command("su", "-c", "rm -f "+tKo).Run()
			zr, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err!=nil { term.Write([]byte("\x1b[31mZip Error\x1b[0m\n")); return }
			var f *zip.File
			for _, zf := range zr.File { if strings.Contains(zf.Name, tVer) && strings.HasSuffix(zf.Name, ".ko") { f=zf; break } }
			if f==nil { for _, zf := range zr.File { if strings.HasSuffix(zf.Name, ".ko") { f=zf; break } } }
			if f!=nil {
				rc, _ := f.Open(); buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
				os.WriteFile(os.TempDir()+"/t.ko", buf.Bytes(), 0644)
				exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", os.TempDir()+"/t.ko", tKo, tKo)).Run()
				if exec.Command("su", "-c", "insmod "+tKo).Run() == nil { term.Write([]byte("\x1b[32m[OK] Loaded.\x1b[0m\n")) } else { term.Write([]byte("\x1b[31m[FAIL] Exists/Error.\x1b[0m\n")) }
			}
		}()
	}

	go func() {
		time.Sleep(2*time.Second); cl := &http.Client{Timeout: 10*time.Second}
		term.Write([]byte("\n\x1b[90m[*] Checking updates...\x1b[0m\n"))
		if r, e := cl.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix())); e==nil && r.StatusCode==200 {
			b, _ := io.ReadAll(r.Body); r.Body.Close()
			if d, e := decryptConfig(string(bytes.TrimSpace(b))); e==nil {
				var c OnlineConfig
				if json.Unmarshal(d, &c)==nil && c.Version!=AppVersion {
					if u, e := url.Parse(c.Link); e==nil { 
						// PAKE CUSTOM MODAL UTK UPDATE JUGA
						showCustomOverlay(overlayContainer, "UPDATE", widget.NewLabel(c.Message), "BATAL", nil, "UPDATE", func(){ a.OpenURL(u) })
					}
				}
			}
		}
	}()

	head := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), container.NewVBox(
		container.NewPadded(createLabel("SIMPLE EXECUTOR", color.White, 16, true)),
		container.NewPadded(container.NewGridWithColumns(3, container.NewVBox(lblK,valK), container.NewVBox(lblS,valS), container.NewVBox(lblR,valR))),
		container.NewPadded(container.NewGridWithColumns(3, 
			widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func(){ 
				showCustomOverlay(overlayContainer, "INJECT", widget.NewLabel("Mulai Inject Driver?"), "BATAL", nil, "MULAI", func(){ doInject() }) 
			}),
			widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func(){ go func(){ exec.Command("su","-c","setenforce "+map[bool]string{true:"0",false:"1"}[CheckSELinux()=="Enforcing"]).Run() }() }),
			widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func(){ term.Clear() }),
		)),
		container.NewPadded(status), widget.NewSeparator(),
	))
	
	cpy := createLabel("Code by TANGSAN", color.Gray{Y:180}, 10, false)
	bot := container.NewVBox(container.NewPadded(cpy), container.NewPadded(container.NewBorder(nil, nil, nil, widget.NewButtonWithIcon("", theme.MailSendIcon(), send), input)))
	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName:"bg.png", StaticContent:bgPng}); bg.FillMode = canvas.ImageFillStretch
	termBox := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll)
	
	var toggleMenu func()
	sideMenu, tFunc := makeSideMenu(w, term, overlayContainer, func(){ toggleMenu() }); toggleMenu = tFunc
	trigger := container.NewHBox(container.NewGridWrap(fyne.NewSize(20, 1000), NewEdgeTrigger(func(){ if !sideMenu.Visible(){toggleMenu()} })), layout.NewSpacer())
	
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName:"fd.png", StaticContent:fdPng}); fdImg.FillMode = canvas.ImageFillContain
	fab := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(60,60), container.NewStack(fdImg, widget.NewButton("", func(){
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error){
			if r!=nil { defer r.Close(); d, _ := io.ReadAll(r); t := os.TempDir()+"/x"; os.WriteFile(t, d, 0755); execute("", true, t, bytes.HasPrefix(d, []byte("\x7fELF"))) }
		}, w).Show()
	})))), widget.NewLabel(" "))

	w.SetContent(container.NewStack(container.NewBorder(head, bot, nil, nil, termBox), trigger, fab, sideMenu, overlayContainer))
	w.ShowAndRun()
}

