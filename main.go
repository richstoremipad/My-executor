package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"os"
	"os/exec"
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
   CONFIG
========================================== */
const GitHubRepo = "https://raw.githubusercontent.com/richstoremipad/My-executor/main/Driver/"
const FlagFile = "/dev/status_driver_aktif"
const TargetDriverName = "5.10_A12" 

/* ==========================================
   TERMINAL UI LOGIC (FIXED COLORS & CLEAR)
========================================== */
type Terminal struct {
	grid     *widget.TextGrid
	scroll   *container.Scroll
	curRow   int
	curCol   int
	curStyle *widget.CustomTextGridStyle
	mutex    sync.Mutex
	reAnsi   *regexp.Regexp
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	defStyle := &widget.CustomTextGridStyle{FGColor: theme.ForegroundColor(), BGColor: color.Transparent}
	// Regex yang lebih lengkap untuk menangkap Clear Screen [2J dan Warna [m
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	return &Terminal{grid: g, scroll: container.NewScroll(g), curStyle: defStyle, reAnsi: re}
}

func (t *Terminal) Clear() {
	t.grid.SetText("")
	t.curRow = 0; t.curCol = 0
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock(); defer t.mutex.Unlock()
	
	// Handle Carriage Return (\r) manual agar progress bar bisa menimpa teks
	raw := string(p)
	if strings.Contains(raw, "\r") && !strings.Contains(raw, "\n") {
		// Ini mode progress bar: Reset kolom ke 0 tapi jangan ganti baris
		t.curCol = 0
		// Hapus sisa baris (opsional, biar bersih)
		t.cleanCurrentLine() 
	}
	
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	
	for len(raw) > 0 {
		loc := t.reAnsi.FindStringIndex(raw)
		if loc == nil { 
			t.printText(raw)
			break 
		}
		if loc[0] > 0 { 
			t.printText(raw[:loc[0]]) 
		}
		
		// Proses Kode ANSI (Warna / Clear)
		ansiCode := raw[loc[0]:loc[1]]
		t.handleAnsiCode(ansiCode)
		
		raw = raw[loc[1]:]
	}
	t.grid.Refresh(); t.scroll.ScrollToBottom()
	return len(p), nil
}

// Membersihkan sisa baris (agar animasi bar tidak meninggalkan jejak)
func (t *Terminal) cleanCurrentLine() {
	if t.curRow < len(t.grid.Rows) {
		row := t.grid.Rows[t.curRow]
		for i := t.curCol; i < len(row.Cells); i++ {
			t.grid.SetCell(t.curRow, i, widget.TextGridCell{Rune: ' '})
		}
	}
}

func (t *Terminal) handleAnsiCode(codeSeq string) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]
	command := codeSeq[len(codeSeq)-1]

	switch command {
	case 'm': // Ganti Warna
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "" || part == "0" { t.curStyle.FGColor = theme.ForegroundColor() }
			if part == "31" || part == "91" { t.curStyle.FGColor = theme.ErrorColor() } // Merah
			if part == "32" || part == "92" { t.curStyle.FGColor = theme.SuccessColor() } // Hijau
			if part == "33" || part == "93" { t.curStyle.FGColor = theme.WarningColor() } // Kuning
			if part == "34" || part == "94" { t.curStyle.FGColor = theme.PrimaryColor() } // Biru
			if part == "36" || part == "96" { t.curStyle.FGColor = color.RGBA{0, 255, 255, 255} } // Cyan
			if part == "37" || part == "97" { t.curStyle.FGColor = color.White }
			if part == "90" { t.curStyle.FGColor = color.Gray{Y: 100} }
		}
	case 'J': // Clear Screen
		// \033[2J atau \033[J
		if strings.Contains(content, "2") || content == "" {
			t.Clear()
		}
	case 'H': // Cursor Home (Kadang dipakai bareng clear)
		t.curRow = 0; t.curCol = 0
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { t.curRow++; t.curCol = 0; continue }
		if char == '\r' { t.curCol = 0; continue }
		
		// Auto expand rows
		for t.curRow >= len(t.grid.Rows) { 
			t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{}) 
		}
		
		// Auto expand cols logic
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

/* ==========================================
   TURBO ANIMATION LOGIC (PING-PONG)
========================================== */

type WriteCounter struct {
	Total         uint64
	ContentLength int64
	OnProgress    func(uint64, int64)
}

func (wc *WriteCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.Total += uint64(n)
	if wc.OnProgress != nil { wc.OnProgress(wc.Total, wc.ContentLength) }
	return n, nil
}

func drawProgressBar(term *Terminal, current uint64, total int64) {
	barLength := 30 // Lebih panjang biar puas
	
	// MODE 1: UKURAN PASTI (Persentase Normal)
	if total > 0 {
		percent := float64(current) / float64(total) * 100
		filledLength := int((percent * float64(barLength)) / 100)
		bar := ""
		for i := 0; i < barLength; i++ {
			if i < filledLength { bar += "█" } else { bar += "░" }
		}
		msg := fmt.Sprintf("\r\x1b[36mDownloading: [%s] %.0f%%   \x1b[0m", bar, percent)
		term.Write([]byte(msg))
		return
	}

	// MODE 2: UNKNOWN SIZE (TURBO BOUNCE ANIMATION)
	// Kita gunakan Waktu (UnixMilli) agar kecepatannya tidak tergantung internet.
	// Semakin kecil pembaginya (/40), semakin cepat gerakannya.
	speed := int64(40) 
	t := time.Now().UnixMilli() / speed
	
	// Logika Ping-Pong (Bolak Balik)
	// Posisi bergerak dari 0 ke (barLength-1) lalu kembali ke 0
	maxPos := barLength - 4 // -4 adalah lebar kotak yang bergerak
	cycle := maxPos * 2
	pos := int(t % int64(cycle))
	
	if pos >= maxPos {
		pos = cycle - pos // Gerak mundur (Kanan ke Kiri)
	}

	// Gambar Bar
	bar := ""
	for i := 0; i < barLength; i++ {
		// Buat kotak bergerak (lebar 4 blok)
		if i >= pos && i < pos+4 {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	
	msg := fmt.Sprintf("\r\x1b[36mDownloading: [%s] Active   \x1b[0m", bar)
	term.Write([]byte(msg))
}

/* ==========================================
   NETWORK & SYSTEM LOGIC
========================================== */

func CheckKernelDriver() bool {
	if _, err := os.Stat(FlagFile); err == nil { return true }
	if exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run() == nil { return true }
	return false 
}

func CheckRootAccess() bool {
	cmd := exec.Command("su", "-c", "id")
	if err := cmd.Run(); err == nil { return true }
	return false
}

func downloadFileRealTime(url string, filepath string, term *Terminal) error {
	exec.Command("su", "-c", "rm -f "+filepath).Run()

	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}

	resp, err := client.Get(url)
	if err != nil { return err }
	defer resp.Body.Close()

	if resp.StatusCode != 200 { return fmt.Errorf("HTTP %d", resp.StatusCode) }

	counter := &WriteCounter{
		ContentLength: resp.ContentLength, 
		OnProgress: func(curr uint64, tot int64) { drawProgressBar(term, curr, tot) },
	}

	localTemp := os.TempDir() + "/kernel_temp.sh"
	out, err := os.Create(localTemp)
	if err != nil { return err }
	
	if _, err = io.Copy(out, io.TeeReader(resp.Body, counter)); err != nil {
		out.Close(); return err
	}
	out.Close()
	term.Write([]byte("\n"))

	moveCmd := fmt.Sprintf("cp %s %s && chmod 777 %s", localTemp, filepath, filepath)
	if err := exec.Command("su", "-c", moveCmd).Run(); err != nil {
		return fmt.Errorf("Gagal memindahkan file ke sistem")
	}
	return nil
}

/* ==========================================
   MAIN APP
========================================== */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())
	exec.Command("su", "-c", "rm -f "+FlagFile).Run()

	w := a.NewWindow("Root Executor PRO")
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()

	term := NewTerminal()
	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")
	status := widget.NewLabel("System: Ready")
	status.TextStyle = fyne.TextStyle{Bold: true}

	var stdin io.WriteCloser

	// --- LABEL STATUS HEADER ---
	kernelLabel := canvas.NewText("KERNEL: CHECKING...", color.RGBA{150, 150, 150, 255})
	kernelLabel.TextSize = 10; kernelLabel.TextStyle = fyne.TextStyle{Bold: true}
	
	rootLabel := canvas.NewText("ROOT: CHECKING...", color.RGBA{150, 150, 150, 255})
	rootLabel.TextSize = 10; rootLabel.TextStyle = fyne.TextStyle{Bold: true}

	updateKernelStatus := func() {
		go func() {
			if CheckKernelDriver() {
				kernelLabel.Text = "KERNEL: DETECTED"
				kernelLabel.Color = color.RGBA{0, 255, 0, 255} 
			} else {
				kernelLabel.Text = "KERNEL: NOT FOUND"
				kernelLabel.Color = color.RGBA{255, 50, 50, 255} 
			}
			kernelLabel.Refresh()
		}()
	}
	updateKernelStatus()

	go func() {
		if CheckRootAccess() {
			rootLabel.Text = "ROOT: GRANTED"
			rootLabel.Color = color.RGBA{0, 255, 0, 255}
		} else {
			rootLabel.Text = "ROOT: DENIED"
			rootLabel.Color = color.RGBA{255, 0, 0, 255}
		}
		rootLabel.Refresh()
	}()

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		status.SetText("Processing File...")
		
		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		
		go func() {
			exec.Command("su", "-c", "rm -f "+target).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() { defer in.Close(); in.Write(data) }()
			copyCmd.Run()
			
			var cmd *exec.Cmd
			if isBinary { cmd = exec.Command("su", "-c", target)
			} else { cmd = exec.Command("su", "-c", "sh "+target) }
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			
			var errPipe error
			stdin, errPipe = cmd.StdinPipe()
			if errPipe != nil { term.Write([]byte("Error: Pipe Fail\n")) }

			cmd.Stdout = term; cmd.Stderr = term
			cmd.Run()
			
			term.Write([]byte("\n\x1b[32m[Execution Finished]\x1b[0m\n"))
			status.SetText("System: Idle")
			stdin = nil
		}()
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			term.Write([]byte(fmt.Sprintf("\x1b[36m> %s\x1b[0m\n", input.Text)))
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* --- AUTO INSTALLER --- */
	autoInstallKernel := func() {
		term.Clear()
		status.SetText("System: Analyzing...")
		
		go func() {
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			updateKernelStatus() 
			
			term.Write([]byte("\x1b[36m╔══════════════════════════════════════╗\x1b[0m\n"))
			term.Write([]byte("\x1b[36m║   REAL-TIME KERNEL DRIVER INSTALLER  ║\x1b[0m\n"))
			term.Write([]byte("\x1b[36m╚══════════════════════════════════════╝\x1b[0m\n"))
			
			out, err := exec.Command("uname", "-r").Output()
			if err != nil { term.Write([]byte("\x1b[31m[X] Kernel Error\x1b[0m\n")); return }
			rawVersion := strings.TrimSpace(string(out))
			term.Write([]byte(fmt.Sprintf("\n[*] Device Kernel: \x1b[33m%s\x1b[0m\n", rawVersion)))

			downloadPath := "/data/local/tmp/kernel_installer.sh"
			var found bool = false

			checkURL := func(url string) bool {
				tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
				client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
				resp, err := client.Head(url)
				if err == nil && resp.StatusCode == 200 { return true }
				return false
			}

			targets := []string{rawVersion + ".sh"}
			parts := strings.Split(rawVersion, "-")
			if len(parts) > 0 { targets = append(targets, parts[0]+".sh") }
			parts = strings.Split(rawVersion, ".")
			if len(parts) >= 2 { targets = append(targets, parts[0]+"."+parts[1]+".sh") }

			for _, t := range targets {
				fullURL := GitHubRepo + t
				term.Write([]byte(fmt.Sprintf("\x1b[90m[?] Checking: %s ... \x1b[0m", t)))
				
				if checkURL(fullURL) {
					term.Write([]byte("\x1b[32m[FOUND]\x1b[0m\n"))
					term.Write([]byte("\n\x1b[97m[*] Starting Download...\x1b[0m\n"))
					if err := downloadFileRealTime(fullURL, downloadPath, term); err != nil {
						term.Write([]byte(fmt.Sprintf("\x1b[31m[!] Download Failed: %v\x1b[0m\n", err)))
					} else {
						found = true
						break 
					}
				} else {
					term.Write([]byte("\x1b[31m[404]\x1b[0m\n"))
				}
			}

			if !found {
				term.Write([]byte("\n\x1b[31m[FATAL] No suitable driver found.\x1b[0m\n"))
				status.SetText("System: Failed")
			} else {
				term.Write([]byte("\x1b[97m[*] Executing Root Installer...\x1b[0m\n"))
				term.Write([]byte("----------------------------------------\n"))
				
				cmd := exec.Command("su", "-c", "sh "+downloadPath)
				cmd.Env = append(os.Environ(), "TERM=xterm-256color")
				
				pipeStdin, _ := cmd.StdinPipe()
				cmd.Stdout = term; cmd.Stderr = term 
				err = cmd.Run()
				pipeStdin.Close()
				
				if err != nil {
					term.Write([]byte(fmt.Sprintf("\n\x1b[31m[EXIT ERROR: %v]\x1b[0m\n", err)))
				} else {
					term.Write([]byte("\n\x1b[32m[PROCESS COMPLETED]\x1b[0m\n"))
				}
				
				time.Sleep(1 * time.Second)
				updateKernelStatus()
				status.SetText("System: Online")
			}
		}()
	}

	installBtn := widget.NewButtonWithIcon("Inject Driver", theme.DownloadIcon(), func() {
		dialog.ShowConfirm("Inject Driver", "Start process?", func(ok bool) { if ok { autoInstallKernel() } }, w)
	})
	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() { term.Clear() })
	checkBtn := widget.NewButtonWithIcon("Scan", theme.SearchIcon(), func() {
		go func() { 
			term.Write([]byte("\n[*] Scanning...\n"))
			out, _ := exec.Command("su", "-c", "lsmod").CombinedOutput()
			term.Write(out)
		}()
	})
	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)
	
	fabBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fabBtn.Importance = widget.HighImportance

	headerLeft := container.NewVBox(
		canvas.NewText("ROOT EXECUTOR PRO", theme.ForegroundColor()), 
		kernelLabel,
		rootLabel,
	)
	header := container.NewBorder(nil, nil, headerLeft, container.NewHBox(installBtn, checkBtn, clearBtn))

	copyright := canvas.NewText("Made by TANGSAN", color.RGBA{R: 255, G: 0, B: 128, A: 255})
	copyright.TextSize = 10; copyright.Alignment = fyne.TextAlignCenter; copyright.TextStyle = fyne.TextStyle{Bold: true}
	
	inputBar := container.NewBorder(nil, nil, nil, sendBtn, input)
	
	bottomSection := container.NewVBox(
		copyright, 
		container.NewPadded(inputBar),
	)

	mainContent := container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()), 
		bottomSection,
		nil, nil, 
		term.scroll,
	)
	
	fabContainer := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), fabBtn, widget.NewLabel(" ")), widget.NewLabel(" "), widget.NewLabel(" "))
	
	finalLayout := container.NewStack(mainContent, fabContainer)

	w.SetContent(finalLayout)
	w.ShowAndRun()
}

