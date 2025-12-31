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

// --- VARIABEL GAME TOOLS ---
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
   SECURITY LOGIC
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
	case "90": return color.Gray{Y: 100}
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
   SYSTEM HELPERS
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
   LOGIKA MLBB (FIXED)
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
		b := make([]byte, n/2)
		rand.Read(b)
		return hex.EncodeToString(b)
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", randHex(56), randHex(4), randHex(4), randHex(4), randHex(12))
}

func runMLBBTask(term *Terminal, taskName string, action func()) {
	term.Write([]byte(fmt.Sprintf("\n\x1b[33m[GAME TOOL] %s...\x1b[0m\n", taskName)))
	go action()
}

// FIX: Download dengan Fallback Root jika Permission Denied
func downloadFile(url string, filepath string) error {
	// 1. Coba cara normal
	resp, err := http.Get(url)
	if err == nil && resp.StatusCode == 200 {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		
		// Coba tulis langsung
		if os.WriteFile(filepath, data, 0644) == nil {
			return nil
		}
		
		// Jika gagal tulis (permission), simpan ke temp lalu pindah pakai Root
		tmpPath := os.TempDir() + "/temp_download"
		os.WriteFile(tmpPath, data, 0644)
		cmd := exec.Command("su", "-c", "cp "+tmpPath+" "+filepath+" && rm "+tmpPath)
		return cmd.Run()
	}
	
	// 2. Jika HTTP Get gagal/diblokir, gunakan CURL via Root (Paling Ampuh)
	cmd := exec.Command("su", "-c", "curl -k -L -o "+filepath+" "+url)
	return cmd.Run()
}

// FIX: Baca file menggunakan Root jika akses biasa ditolak
func parseAccountFile(path string) ([]string, []string, []string, error) {
	var content string
	
	b, err := os.ReadFile(path)
	if err == nil {
		content = string(b)
	} else {
		// Fallback baca pakai root
		out, err2 := exec.Command("su", "-c", "cat \""+path+"\"").Output()
		if err2 != nil {
			return nil, nil, nil, errors.New("Gagal baca file (Permission/Root)")
		}
		content = string(out)
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
			displays = append(displays, name) // HANYA NAMA (Sesuai Request)
		}
	}
	
	if len(ids) == 0 { return nil, nil, nil, errors.New("File kosong") }
	return ids, names, displays, nil
}

func applyDeviceIDLogic(term *Terminal, targetID, targetPkg, targetAppName, customAccName string) {
	targetID = cleanString(targetID)
	targetFile := fmt.Sprintf("/data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml", targetPkg, targetPkg)

	term.Write([]byte(fmt.Sprintf("\x1b[36mTarget: %s\nID: %s\x1b[0m\n", targetAppName, targetID)))
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

// --- MENU GAME TOOLS DENGAN TAMPILAN PILIH & BATAL ---
func makeSideMenu(w fyne.Window, term *Terminal, onClose func()) (*fyne.Container, func()) {
	dimmer := canvas.NewRectangle(color.RGBA{0, 0, 0, 150})
	btnDimmer := widget.NewButton("", onClose)
	btnDimmer.Importance = widget.LowImportance
	dimmerContainer := container.NewStack(dimmer, btnDimmer)
	bgMenu := canvas.NewRectangle(theme.BackgroundColor())

	lblTitle := widget.NewLabelWithStyle("GAME TOOLS", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})

	// SELECT GAME
	selGame := widget.NewSelect(AppNames, func(s string) {
		for i, v := range AppNames { if v == s { SelectedGameIdx = i } }
	})
	selGame.SetSelected(AppNames[0])
	cardTarget := widget.NewCard("Target Game", "", container.NewPadded(selGame))

	// LOGIN AKUN (MODIFIKASI TAMPILAN LIST)
	btnLogin := widget.NewButtonWithIcon("Login Akun", theme.LoginIcon(), func() {
		onClose()
		dialog.ShowCustomConfirm("Pilih Metode Login", "Online", "Offline", widget.NewLabel("Sumber Akun:"), func(isOnline bool) {
			
			// Logic Handler
			handleFile := func(path string) {
				ids, rNames, dList, err := parseAccountFile(path)
				if err != nil {
					term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] %s\x1b[0m\n", err.Error())))
					return 
				}

				// Variabel Pilihan
				selectedIndex := -1
				
				// List UI
				listWidget := widget.NewList(
					func() int { return len(dList) },
					func() fyne.CanvasObject { 
						// Template Item
						icon := widget.NewIcon(theme.ConfirmIcon) // Icon Ceklis (Hidden by default)
						icon.Hide()
						label := widget.NewLabel("Template Name")
						label.TextStyle = fyne.TextStyle{Bold: true}
						return container.NewHBox(icon, label)
					},
					func(i int, o fyne.CanvasObject) { 
						box := o.(*fyne.Container)
						icon := box.Objects[0].(*widget.Icon)
						lbl := box.Objects[1].(*widget.Label)
						
						lbl.SetText(dList[i])
						
						// Tampilkan Ceklis jika dipilih
						if i == selectedIndex {
							icon.Show()
							lbl.TextStyle.Italic = true // Visual cue tambahan
						} else {
							icon.Hide()
							lbl.TextStyle.Italic = false
						}
					},
				)

				// Container List agar bisa refresh
				listContainer := container.NewStack(listWidget)

				// Dialog Custom manual
				var popup *widget.PopUp
				
				// Tombol Bawah
				btnPilih := widget.NewButton("PILIH", func() {
					if selectedIndex >= 0 {
						popup.Hide()
						runMLBBTask(term, "Login: "+rNames[selectedIndex], func() {
							applyDeviceIDLogic(term, ids[selectedIndex], PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], rNames[selectedIndex])
							exec.Command("su", "-c", fmt.Sprintf("am start -n %s/com.moba.unityplugin.MobaGameUnityActivity", PackageNames[SelectedGameIdx])).Run()
							if isOnline { os.Remove(OnlineAccFile) }
						})
					}
				})
				btnPilih.Importance = widget.HighImportance
				btnPilih.Disable() // Disable sampai ada yang dipilih

				btnBatal := widget.NewButton("BATAL", func() {
					popup.Hide()
				})
				btnBatal.Importance = widget.DangerImportance

				// Event saat Item diklik
				listWidget.OnSelected = func(id int) {
					selectedIndex = id
					btnPilih.Enable()
					listWidget.Refresh() // Refresh UI untuk update ceklis
				}

				// Layout Popup
				content := container.NewBorder(
					widget.NewLabelWithStyle("DAFTAR AKUN", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
					container.NewGridWithColumns(2, btnBatal, btnPilih),
					nil, nil,
					listContainer,
				)
				
				popup = widget.NewModalPopUp(container.NewGridWrap(fyne.NewSize(320, 500), content), w.Canvas())
				popup.Show()
			}

			// PROSES DOWNLOAD / READ
			if isOnline {
				defaultUrl := ""
				if content, err := os.ReadFile(UrlConfigFile); err == nil {
					defaultUrl = cleanString(string(content))
				}

				if defaultUrl != "" {
					go func() {
						term.Write([]byte(fmt.Sprintf("\x1b[33m[DL] URL tersimpan: %s\x1b[0m\n", defaultUrl)))
						if err := downloadFile(defaultUrl, OnlineAccFile); err == nil {
							term.Write([]byte("\x1b[32m[DL] Sukses.\x1b[0m\n"))
							handleFile(OnlineAccFile)
						} else {
							term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] Gagal Download: %s\x1b[0m\n", err.Error())))
						}
					}()
				} else {
					entryUrl := widget.NewEntry(); entryUrl.SetPlaceHolder("https://...")
					dialog.ShowCustomConfirm("Input URL", "DL", "Batal", entryUrl, func(dl bool) {
						if dl && entryUrl.Text != "" {
							os.WriteFile(UrlConfigFile, []byte(entryUrl.Text), 0644)
							go func() {
								term.Write([]byte("\x1b[33m[DL] Mendownload...\x1b[0m\n"))
								if err := downloadFile(entryUrl.Text, OnlineAccFile); err == nil {
									handleFile(OnlineAccFile)
								} else {
									term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] Gagal Download: %s\x1b[0m\n", err.Error())))
								}
							}()
						}
					}, w)
				}
			} else {
				term.Write([]byte("\x1b[33m[INFO] Mode Offline\x1b[0m\n"))
				handleFile(AccountFile)
			}
		}, w)
	})
	
	btnReset := widget.NewButtonWithIcon("Reset ID", theme.ViewRefreshIcon(), func() {
		onClose()
		dialog.ShowConfirm("Reset ID", "Ganti ID Random?", func(b bool) {
			if b {
				runMLBBTask(term, "Reset ID Random", func() {
					newID := generateRandomID()
					applyDeviceIDLogic(term, newID, PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], "GUEST/NEW")
				})
			}
		}, w)
	})
	
	cardAccount := widget.NewCard("Akun Manager", "", container.NewPadded(container.NewGridWithColumns(2, btnLogin, btnReset)))

	// UTILS
	btnCopy := widget.NewButtonWithIcon("Salin ID", theme.ContentCopyIcon(), func() {
		onClose()
		selSrc := widget.NewSelect(AppNames, nil); selSrc.PlaceHolder = "Pilih Sumber"
		dialog.ShowCustomConfirm("Salin ID", "Salin", "Batal", container.NewVBox(widget.NewLabel("Dari:"), selSrc), func(b bool) {
			if b && selSrc.Selected != "" {
				srcIdx := 0
				for i, v := range AppNames { if v == selSrc.Selected { srcIdx = i } }
				runMLBBTask(term, "Salin ID", func() {
					cmdStr := fmt.Sprintf("sed -n 's/.*<string name=\"JsonDeviceID\">\\([^<]*\\)<.*/\\1/p' /data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml | head -n 1", PackageNames[srcIdx], PackageNames[srcIdx])
					out, err := exec.Command("su", "-c", cmdStr).Output()
					srcID := cleanString(string(out))
					if err == nil && len(srcID) > 5 {
						applyDeviceIDLogic(term, srcID, PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], "HASIL COPY")
					} else {
						term.Write([]byte("\x1b[31m[ERR] Gagal/Kosong.\x1b[0m\n"))
					}
				})
			}
		}, w)
	})

	btnSel := widget.NewButtonWithIcon("Switch SELinux", theme.InfoIcon(), func() {
		onClose()
		runMLBBTask(term, "Toggle SELinux", func() {
			exec.Command("su", "-c", "setenforce 0").Run()
			term.Write([]byte("\x1b[33m[INFO] Perintah setenforce 0 dikirim.\x1b[0m\n"))
		})
	})
	
	cardUtils := widget.NewCard("System", "", container.NewPadded(container.NewVBox(btnCopy, btnSel)))

	btnClean := widget.NewButtonWithIcon("Clear Terminal", theme.ContentClearIcon(), func() { term.Clear(); onClose() })
	btnExit := widget.NewButtonWithIcon("Keluar", theme.LogoutIcon(), func() { os.Exit(0) }); btnExit.Importance = widget.DangerImportance

	scrollContent := container.NewVBox(
		container.NewPadded(lblTitle), widget.NewSeparator(),
		cardTarget, cardAccount, cardUtils,
		widget.NewSeparator(), btnClean, layout.NewSpacer(), btnExit,
	)
	
	panel := container.NewStack(bgMenu, container.NewPadded(container.NewVScroll(scrollContent)))
	slideContainer := container.NewHBox(container.NewGridWrap(fyne.NewSize(280, 700), panel))
	finalMenu := container.NewStack(dimmerContainer, slideContainer); finalMenu.Hide()

	toggle := func() { if finalMenu.Visible() { finalMenu.Hide() } else { finalMenu.Show(); finalMenu.Refresh() } }
	return finalMenu, toggle
}

/* ===============================
              MAIN UI
================================ */
func main() {
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
				
				// Panggil Deteksi Kallsyms
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

	var overlayContainer *fyne.Container // FIX
	
	// FUNGSI POPUP (UPDATED: LOGIKA WARNA TOMBOL RETRY)
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
		
		// LOGIKA WARNA:
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

	// DEFINISI FUNGSI UPDATE (WARNA FIX: 90 = Abu-abu)
	var checkUpdate func()
	checkUpdate = func() {
		overlayContainer.Hide()
		
		time.Sleep(500 * time.Millisecond) 
		if strings.Contains(ConfigURL, "GANTI") { term.Write([]byte("\n\x1b[33m[WARN] ConfigURL!\x1b[0m\n")); return }
		// FIX WARNA: \x1b[90m
		term.Write([]byte("\n\x1b[90m[*] Checking updates...\x1b[0m\n"))
		
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix()))
		
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body); resp.Body.Close()
			if dec, err := decryptConfig(string(bytes.TrimSpace(body))); err == nil {
				var cfg OnlineConfig
				if json.Unmarshal(dec, &cfg) == nil {
					if cfg.Version != "" && cfg.Version != AppVersion {
						showModal("UPDATE", cfg.Message, "UPDATE", func() { 
							if u, e := url.Parse(cfg.Link); e == nil { app.New().OpenURL(u) } 
						}, false, true)
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
		showModal("INJECT", "Mulai Inject Driver?", "MULAI", autoInstallKernel, false, false) 
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
	
	sideMenuContainer, toggleFunc := makeSideMenu(w, term, func() { toggleMenu() })
	toggleMenu = toggleFunc

	edgeTrigger := NewEdgeTrigger(func() {
		if !sideMenuContainer.Visible() { toggleMenu() }
	})
	triggerZone := container.NewHBox(container.NewGridWrap(fyne.NewSize(20, 1000), edgeTrigger), layout.NewSpacer())

	overlayContainer = container.NewStack(); overlayContainer.Hide()

	w.SetContent(container.NewStack(
		container.NewBorder(header, bottom, nil, nil, termBox), // Main UI
		triggerZone,       // Layer 2
		fab,               // Layer 3
		sideMenuContainer, // Layer 4
		overlayContainer,  // Layer 5
	))

	w.ShowAndRun()
}

