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
   TERMINAL UI LOGIC
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
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	return &Terminal{grid: g, scroll: container.NewScroll(g), curStyle: defStyle, reAnsi: re}
}

func (t *Terminal) Clear() {
	t.grid.SetText("")
	t.curRow = 0; t.curCol = 0
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock(); defer t.mutex.Unlock()
	raw := strings.ReplaceAll(string(p), "\r\n", "\n")
	for len(raw) > 0 {
		loc := t.reAnsi.FindStringIndex(raw)
		if loc == nil { t.printText(raw); break }
		if loc[0] > 0 { t.printText(raw[:loc[0]]) }
		t.handleAnsiCode(raw[loc[0]:loc[1]])
		raw = raw[loc[1]:]
	}
	t.grid.Refresh(); t.scroll.ScrollToBottom()
	return len(p), nil
}

func (t *Terminal) handleAnsiCode(codeSeq string) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]
	if codeSeq[len(codeSeq)-1] == 'm' {
		if content == "0" || content == "" { t.curStyle.FGColor = theme.ForegroundColor() }
		if content == "31" || content == "91" { t.curStyle.FGColor = theme.ErrorColor() }
		if content == "32" || content == "92" { t.curStyle.FGColor = theme.SuccessColor() }
		if content == "33" || content == "93" { t.curStyle.FGColor = theme.WarningColor() }
		if content == "36" || content == "96" { t.curStyle.FGColor = color.RGBA{0, 255, 255, 255} }
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { t.curRow++; t.curCol = 0; continue }
		if char == '\r' { t.curCol = 0; continue }
		for t.curRow >= len(t.grid.Rows) { t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{}) }
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
   REAL-TIME PROGRESS LOGIC
========================================== */

type WriteCounter struct {
	Total         uint64
	ContentLength uint64
	OnProgress    func(float64, string)
}

func (wc *WriteCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.Total += uint64(n)
	wc.PrintProgress()
	return n, nil
}

func (wc *WriteCounter) PrintProgress() {
	percent := float64(wc.Total) / float64(wc.ContentLength) * 100
	currentMB := float64(wc.Total) / 1024 / 1024
	totalMB := float64(wc.ContentLength) / 1024 / 1024
	statusStr := fmt.Sprintf("%.2f/%.2f MB", currentMB, totalMB)
	if wc.OnProgress != nil { wc.OnProgress(percent, statusStr) }
}

func drawRealTimeBar(term *Terminal, percent float64, info string) {
	barLength := 20
	filledLength := int((percent * float64(barLength)) / 100)
	bar := ""
	for i := 0; i < barLength; i++ {
		if i < filledLength { bar += "█" } else { bar += "░" }
	}
	msg := fmt.Sprintf("\r\x1b[36mDownloading: [%s] %.0f%% (%s)   \x1b[0m", bar, percent, info)
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

func downloadFileRealTime(url string, filepath string, term *Terminal) error {
	exec.Command("su", "-c", "rm -f "+filepath).Run()

	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}

	resp, err := client.Get(url)
	if err != nil { return err }
	defer resp.Body.Close()

	if resp.StatusCode != 200 { return fmt.Errorf("HTTP %d", resp.StatusCode) }

	counter := &WriteCounter{
		ContentLength: uint64(resp.ContentLength),
		OnProgress: func(p float64, info string) { drawRealTimeBar(term, p, info) },
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

	kernelLabel := canvas.NewText("KERNEL: CHECKING...", color.RGBA{150, 150, 150, 255})
	kernelLabel.TextSize = 10; kernelLabel.TextStyle = fyne.TextStyle{Bold: true}

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

	// --- FUNGSI RUN FILE LOKAL (Memperbaiki Error "bytes" & "layout" unused) ---
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		status.SetText("Processing File...")
		
		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		
		// "bytes" sekarang terpakai di sini
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
			
			stdin, _ := cmd.StdinPipe()
			cmd.Stdout = term; cmd.Stderr = term
			cmd.Run()
			
			term.Write([]byte("\n\x1b[32m[Execution Finished]\x1b[0m\n"))
			status.SetText("System: Idle")
			stdin = nil
		}()
	}

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

	/* --- UI LAYOUT --- */
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

	// Floating Action Button (Folder) - Ini yang membutuhkan "layout"
	fabBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fabBtn.Importance = widget.HighImportance

	// Container untuk Header
	header := container.NewBorder(nil, nil, container.NewVBox(canvas.NewText("ROOT EXECUTOR PRO", theme.ForegroundColor()), kernelLabel), container.NewHBox(installBtn, checkBtn, clearBtn))
	
	// Layout Utama
	mainContent := container.NewBorder(container.NewVBox(header, widget.NewSeparator()), nil, nil, nil, term.scroll)
	
	// Layout Tumpuk (Stack) agar FAB melayang di atas terminal
	// "layout" sekarang terpakai di sini (layout.NewSpacer)
	fabContainer := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), fabBtn, widget.NewLabel(" ")), widget.NewLabel(" "), widget.NewLabel(" "))
	
	finalLayout := container.NewStack(mainContent, fabContainer)

	w.SetContent(finalLayout)
	w.ShowAndRun()
}

