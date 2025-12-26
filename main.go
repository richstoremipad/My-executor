package main

import (
	"bytes"
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
   TERMINAL LOGIC & SNIFFER SYSTEM
========================================== */

type Terminal struct {
	grid     *widget.TextGrid
	scroll   *container.Scroll
	curRow   int
	curCol   int
	curStyle *widget.CustomTextGridStyle
	mutex    sync.Mutex
	reAnsi   *regexp.Regexp
	reURL    *regexp.Regexp // Regex khusus deteksi URL
	reIP     *regexp.Regexp // Regex khusus deteksi IP
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}
	// Regex Pre-compile untuk performa sniffer
	return &Terminal{
		grid:     g,
		scroll:   container.NewScroll(g),
		curRow:   0,
		curCol:   0,
		curStyle: defStyle,
		reAnsi:   regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`),
		reURL:    regexp.MustCompile(`https?://[a-zA-Z0-9./?=_-]+`),
		reIP:     regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`),
	}
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
	t.grid.SetText("")
	t.curRow = 0
	t.curCol = 0
}

// === FUNGSI UTAMA SNIFFER ===
func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	raw := string(p)
	// Kita pecah per baris agar filter bekerja akurat
	lines := strings.Split(raw, "\n")

	for _, line := range lines {
		// Bersihkan karakter kosong/spasi berlebih
		cleanLine := strings.TrimSpace(line)
		if cleanLine == "" { continue }

		isInteresting := false
		
		// 1. CEK APAKAH ADA URL / IP (Indikasi Server Address)
		if t.reURL.MatchString(cleanLine) || t.reIP.MatchString(cleanLine) {
			// Tambahkan prefix SNIFF dan beri warna CYAN
			line = "\x1b[36m[SNIFF URL] " + line + "\x1b[0m"
			isInteresting = true
		} else if strings.Contains(strings.ToLower(cleanLine), "host:") || strings.Contains(strings.ToLower(cleanLine), "connect") {
			line = "\x1b[36m[SNIFF NET] " + line + "\x1b[0m"
			isInteresting = true
		}

		// 2. CEK APAKAH ADA RESPON DATA (JSON / Key / Token)
		// Deteksi kurung kurawal '{' (JSON) atau kata kunci respons
		if strings.Contains(cleanLine, "{") || strings.Contains(cleanLine, "}") || 
		   strings.Contains(cleanLine, "response") || strings.Contains(cleanLine, "token") || 
		   strings.Contains(cleanLine, "key") || strings.Contains(cleanLine, "auth") ||
		   strings.Contains(cleanLine, "expire") {
			
			// Tambahkan prefix DATA dan beri warna HIJAU TERANG
			line = "\x1b[92m[SNIFF DATA] " + line + "\x1b[0m"
			isInteresting = true
		}

		// 3. FILTER LOGIC
		// Jika baris tersebut MENARIK (Network/Data), kita cetak.
		// Jika tidak (misal cuma UI biasa), kita BUANG (skip).
		if isInteresting {
			// Proses pencetakan (Ansi Parsing seperti biasa)
			line = strings.ReplaceAll(line, "\r", "") // Bersihkan CR
			
			// Render ke TextGrid
			tempLine := line
			for len(tempLine) > 0 {
				loc := t.reAnsi.FindStringIndex(tempLine)
				if loc == nil {
					t.printText(tempLine)
					break
				}
				if loc[0] > 0 {
					t.printText(tempLine[:loc[0]])
				}
				ansiCode := tempLine[loc[0]:loc[1]]
				t.handleAnsiCode(ansiCode)
				tempLine = tempLine[loc[1]:]
			}
			// Tambah enter manual karena kita split by newline
			t.printText("\n")
		}
	}

	t.grid.Refresh()
	t.scroll.ScrollToBottom()
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
			if part == "" || part == "0" {
				t.curStyle.FGColor = theme.ForegroundColor()
			} else {
				col := ansiToColor(part)
				if col != nil {
					t.curStyle.FGColor = col
				}
			}
		}
	case 'J':
		if strings.Contains(content, "2") {
			t.Clear()
		}
	case 'H':
		t.curRow = 0
		t.curCol = 0
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' {
			t.curRow++
			t.curCol = 0
			continue
		}
		for t.curRow >= len(t.grid.Rows) {
			t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}})
		}
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}
		cellStyle := *t.curStyle
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{
			Rune:  char,
			Style: &cellStyle,
		})
		t.curCol++
	}
}

func drawProgressBar(term *Terminal, label string, percent int, colorCode string) {
	// Fitur ini di-mute oleh sniffer karena tidak mengandung URL/Data
	// Tapi kita biarkan kodenya ada untuk kompatibilitas
	msg := fmt.Sprintf("\r%s %s %d%%", colorCode, label, percent)
	term.Write([]byte(msg))
}

func CheckKernelDriver() bool {
	if _, err := os.Stat(FlagFile); err == nil { return true }
	cmd := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName)
	if err := cmd.Run(); err == nil { return true }
	return false 
}

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

/* ===============================
              MAIN UI
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	exec.Command("su", "-c", "rm -f "+FlagFile).Run()

	w := a.NewWindow("Root Executor - SNIFFER MODE") // Judul diganti sedikit
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()

	term := NewTerminal()
	
	// Pesan Awal Sniffer
	term.Write([]byte("\x1b[33m[SYSTEM] SNIFFER MODE ACTIVE\x1b[0m\n"))
	term.Write([]byte("\x1b[33m[SYSTEM] Waiting for network traffic...\x1b[0m\n"))

	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")
	status := widget.NewLabel("System: Sniffer Ready")
	status.TextStyle = fyne.TextStyle{Bold: true}
	var stdin io.WriteCloser

	kernelLabel := canvas.NewText("KERNEL: CHECKING...", color.RGBA{150, 150, 150, 255})
	kernelLabel.TextSize = 10
	kernelLabel.TextStyle = fyne.TextStyle{Bold: true}
	kernelLabel.Alignment = fyne.TextAlignLeading

	updateKernelStatus := func() {
		go func() {
			isLoaded := CheckKernelDriver()
			w.Canvas().Refresh(kernelLabel)
			if isLoaded {
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

	autoInstallKernel := func() {
		term.Write([]byte("\n[INFO] Starting Kernel Injection... (Hidden Logs)\n"))
		status.SetText("System: Installing...")
		go func() {
			// Code install driver tetap jalan di background
			// Tapi outputnya akan difilter oleh Sniffer di Terminal.Write
			// Jadi user hanya akan melihat jika ada URL download muncul.
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			updateKernelStatus() 
			
			// ... (Proses download berjalan normal) ...
			// Simulasi URL agar terlihat di Sniffer:
			term.Write([]byte("Requesting: " + GitHubRepo + "\n"))

			out, err := exec.Command("uname", "-r").Output()
			if err != nil { return }
			rawVersion := strings.TrimSpace(string(out))
			
			downloadPath := "/data/local/tmp/temp_kernel_dl" 
			targetFile := "/data/local/tmp/kernel_installer.sh"
			
			url1 := GitHubRepo + rawVersion + ".sh"
			term.Write([]byte("Checking: " + url1 + "\n")) // Ini akan muncul cyan
			
			err, _ = downloadFile(url1, downloadPath)
			if err == nil {
				exec.Command("su", "-c", "mv "+downloadPath+" "+targetFile).Run()
				exec.Command("su", "-c", "chmod 777 "+targetFile).Run()
				cmd := exec.Command("su", "-c", "sh "+targetFile)
				cmd.Env = append(os.Environ(), "TERM=xterm-256color")
				cmd.Stdout = term; cmd.Stderr = term
				cmd.Run()
				
				time.Sleep(1 * time.Second)
				updateKernelStatus()
				status.SetText("System: Online")
			} else {
				status.SetText("System: Failed")
			}
		}()
	}

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		term.Write([]byte("\x1b[33m[SNIFFER] Monitoring Output...\x1b[0m\n"))
		status.SetText("Status: Sniffing...")
		
		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		
		go func() {
			// Environment Spoofing agar aplikasi jalan
			exec.Command("su", "-c", "touch "+FlagFile).Run()
			exec.Command("su", "-c", "chmod 777 "+FlagFile).Run()
			exec.Command("su", "-c", "echo 'SUCCESS' > "+FlagFile).Run()

			exec.Command("su", "-c", "rm -f "+target).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() { defer in.Close(); in.Write(data) }()
			copyCmd.Run()
			
			var cmd *exec.Cmd
			if isBinary { 
				cmd = exec.Command("su", "-c", target)
			} else { 
				cmd = exec.Command("su", "-c", "sh "+target) 
			}
			
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			
			// Hubungkan output ke terminal (yang sudah ada filter Sniffer-nya)
			cmd.Stdout = term
			cmd.Stderr = term

			stdin, _ = cmd.StdinPipe()
			cmd.Run()
			
			status.SetText("Status: Idle")
			stdin = nil
		}()
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			// Input user tidak perlu di sniff
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	titleText := canvas.NewText("ROOT EXECUTOR PRO", theme.ForegroundColor())
	titleText.TextSize = 16
	titleText.TextStyle = fyne.TextStyle{Bold: true}

	headerLeft := container.NewVBox(titleText, kernelLabel)

	checkBtn := widget.NewButtonWithIcon("Scan", theme.SearchIcon(), func() {
		term.Write([]byte("\nScanning...\n"))
		go func() {
			cmd := exec.Command("su", "-c", "lsmod")
			output, _ := cmd.CombinedOutput()
			// Output lsmod biasanya tidak ada URL, jadi mungkin akan kosong di layar sniffer
			// Kecuali kita paksa print:
			term.Write(output)
		}()
	})

	installBtn := widget.NewButtonWithIcon("Inject Driver", theme.DownloadIcon(), func() {
		dialog.ShowConfirm("Inject Driver", "Start automatic injection process?", func(ok bool) {
			if ok { autoInstallKernel() }
		}, w)
	})
	
	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() { term.Clear() })
	headerRight := container.NewHBox(installBtn, checkBtn, clearBtn)
	
	headerBar := container.NewBorder(nil, nil, container.NewPadded(headerLeft), headerRight)
	topSection := container.NewVBox(headerBar, container.NewPadded(status), widget.NewSeparator())
	
	copyright := canvas.NewText("SYSTEM: ROOT ACCESS GRANTED", color.RGBA{R: 0, G: 255, B: 0, A: 255})
	copyright.TextSize = 10; copyright.Alignment = fyne.TextAlignCenter; copyright.TextStyle = fyne.TextStyle{Monospace: true}
	sendBtn := widget.NewButtonWithIcon("Send", theme.MailSendIcon(), send)
	inputContainer := container.NewPadded(container.NewBorder(nil, nil, nil, sendBtn, input))
	bottomSection := container.NewVBox(copyright, inputContainer)

	mainLayer := container.NewBorder(topSection, bottomSection, nil, nil, term.scroll)
	
	fabBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fabBtn.Importance = widget.HighImportance

	fabContainer := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), fabBtn, widget.NewLabel(" ")), widget.NewLabel(" "), widget.NewLabel(" "))
	
	w.SetContent(container.NewStack(mainLayer, fabContainer))
	w.ShowAndRun()
}

