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
// Link RAW GitHub (Pastikan Public)
const GitHubRepo = "https://raw.githubusercontent.com/richstoremipad/My-executor/main/Driver/"
const TargetDriverName = "Driver" 

/* ==========================================
   TERMINAL LOGIC
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
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	return &Terminal{
		grid:     g,
		scroll:   container.NewScroll(g),
		curRow:   0,
		curCol:   0,
		curStyle: defStyle,
		reAnsi:   re,
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
	case "90": return color.Gray{Y: 150}
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

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	raw := string(p)
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
		ansiCode := raw[loc[0]:loc[1]]
		t.handleAnsiCode(ansiCode)
		raw = raw[loc[1]:]
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
		if char == '\r' {
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

/* ===============================
   HELPER FUNCTIONS (UPDATED)
================================ */

func CheckKernelDriver() bool {
	cmd := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName)
	err := cmd.Run()
	return err == nil 
}

// [FIXED] FUNGSI DOWNLOAD dengan PATH ABSOLUT
func downloadFile(url string, filepath string) (error, string) {
	// Hapus file lama agar tidak korup (pakai root biar pasti kehapus)
	exec.Command("su", "-c", "rm -f "+filepath).Run()

	// METODE 1: CURL (Recommended untuk Root)
	// Kita pastikan curl menulis ke path absolut (/data/local/tmp/...)
	// -k: Skip SSL
	// -L: Follow Redirect
	// -f: Fail silently on HTTP error (404)
	cmdStr := fmt.Sprintf("curl -k -L -f --connect-timeout 10 -o %s %s", filepath, url)
	cmd := exec.Command("su", "-c", cmdStr)
	err := cmd.Run()
	
	if err == nil {
		// Cek apakah file benar-benar terisi
		// Kita cek pakai 'ls' via root karena aplikasi biasa mungkin gak bisa baca stat di tmp
		checkCmd := exec.Command("su", "-c", "[ -s "+filepath+" ]")
		if checkCmd.Run() == nil {
			return nil, "Success via CURL"
		}
	}

	// METODE 2: Go HTTP Client (Fallback)
	// Jika CURL gagal, kita coba download pakai Go.
	// TAPI: App biasa mungkin gak bisa tulis ke /data/local/tmp.
	// Jadi kita harus pipe streamnya ke perintah 'su' agar bisa ditulis.
	
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err, "HTTP Init Fail"
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return err, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode), fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	// [TRICKY] Pipe isi download langsung ke file tujuan via Root
	// Ini menghindari "File Create Err" karena permission denied
	writeCmd := exec.Command("su", "-c", "cat > "+filepath)
	stdin, err := writeCmd.StdinPipe()
	if err != nil {
		return err, "Pipe Err"
	}

	go func() {
		defer stdin.Close()
		io.Copy(stdin, resp.Body)
	}()

	if err := writeCmd.Run(); err != nil {
		return err, "Write Err (Root)"
	}
	
	return nil, "Success via HTTP+Root"
}

/* ===============================
              MAIN UI
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Root Executor")
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()

	term := NewTerminal()
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik perintah...")
	status := widget.NewLabel("Status: Siap")
	status.TextStyle = fyne.TextStyle{Bold: true}
	var stdin io.WriteCloser

	// Label Status Kernel
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

	/* --- LOGIKA AUTO INSTALL KERNEL --- */
	autoInstallKernel := func() {
		term.Clear()
		status.SetText("Status: Auto Install Kernel")
		
		go func() {
			term.Write([]byte("\x1b[36m[*] Detecting Device Kernel...\x1b[0m\n"))
			out, err := exec.Command("uname", "-r").Output()
			if err != nil {
				term.Write([]byte("\x1b[31m[!] Error detecting kernel.\x1b[0m\n"))
				return
			}
			
			rawVersion := strings.TrimSpace(string(out))
			term.Write([]byte(fmt.Sprintf(" > Kernel Version: \x1b[33m%s\x1b[0m\n\n", rawVersion)))

			// [FIXED] Gunakan Absolute Path di /data/local/tmp
			downloadPath := "/data/local/tmp/temp_kernel_dl" 
			targetFile := "/data/local/tmp/kernel_installer.sh"
			
			var downloadUrl string
			var found bool = false
			var failReason string

			// 1. Cek Full Name
			url1 := GitHubRepo + rawVersion + ".sh"
			term.Write([]byte(fmt.Sprintf("\x1b[90m[1] Checking: %s\x1b[0m\n", url1)))
			err, reason := downloadFile(url1, downloadPath)
			if err == nil {
				downloadUrl = url1
				found = true
				term.Write([]byte(fmt.Sprintf("    -> \x1b[32mOK (%s)\x1b[0m\n", reason)))
			} else {
				term.Write([]byte(fmt.Sprintf("    -> \x1b[31mFail: %s\x1b[0m\n", reason)))
				failReason = reason
			}

			// 2. Cek Short Version (Sebelum tanda -)
			if !found {
				parts := strings.Split(rawVersion, "-")
				if len(parts) > 0 {
					shortVersion := parts[0]
					url2 := GitHubRepo + shortVersion + ".sh"
					term.Write([]byte(fmt.Sprintf("\x1b[90m[2] Checking: %s\x1b[0m\n", url2)))
					err, reason = downloadFile(url2, downloadPath)
					if err == nil {
						downloadUrl = url2
						found = true
						term.Write([]byte(fmt.Sprintf("    -> \x1b[32mOK (%s)\x1b[0m\n", reason)))
					} else {
						term.Write([]byte(fmt.Sprintf("    -> \x1b[31mFail: %s\x1b[0m\n", reason)))
						failReason = reason
					}
				}
			}

			// 3. Cek Major Version (misal 4.19)
			if !found {
				parts := strings.Split(rawVersion, ".")
				if len(parts) >= 2 {
					majorVersion := parts[0] + "." + parts[1]
					url3 := GitHubRepo + majorVersion + ".sh"
					term.Write([]byte(fmt.Sprintf("\x1b[90m[3] Checking: %s\x1b[0m\n", url3)))
					err, reason = downloadFile(url3, downloadPath)
					if err == nil {
						downloadUrl = url3
						found = true
						term.Write([]byte(fmt.Sprintf("    -> \x1b[32mOK (%s)\x1b[0m\n", reason)))
					} else {
						term.Write([]byte(fmt.Sprintf("    -> \x1b[31mFail: %s\x1b[0m\n", reason)))
						failReason = reason
					}
				}
			}

			// Eksekusi
			if !found {
				term.Write([]byte("\n\x1b[31m[X] Script Not Found / Download Failed.\x1b[0m\n"))
				term.Write([]byte(fmt.Sprintf("\x1b[90m    Last Error: %s\x1b[0m\n", failReason)))
				term.Write([]byte("\x1b[90m    Pastikan file: " + rawVersion + ".sh ada di GitHub.\x1b[0m\n"))
				status.SetText("Status: Gagal")
			} else {
				term.Write([]byte(fmt.Sprintf("\n\x1b[32m[V] Script Found: %s\x1b[0m\n", downloadUrl)))
				term.Write([]byte("[*] Installing...\n"))
				
				// Rename hasil download ke target file eksekusi
				exec.Command("su", "-c", "mv "+downloadPath+" "+targetFile).Run()
				exec.Command("su", "-c", "chmod 777 "+targetFile).Run()

				cmd := exec.Command("su", "-c", "sh "+targetFile)
				cmd.Env = append(os.Environ(), "TERM=xterm-256color")
				
				var pipeStdin io.WriteCloser
				pipeStdin, _ = cmd.StdinPipe()
				cmd.Stdout = term
				cmd.Stderr = term
				
				err = cmd.Run()
				
				if err != nil {
					term.Write([]byte(fmt.Sprintf("\n\x1b[31m[EXIT ERROR: %v]\x1b[0m\n", err)))
				} else {
					term.Write([]byte("\n\x1b[32m[INSTALLATION COMPLETE]\x1b[0m\n"))
				}
				pipeStdin.Close()
				
				time.Sleep(1 * time.Second)
				updateKernelStatus()
				status.SetText("Status: Selesai")
			}
		}()
	}

	/* --- FUNGSI LOCAL FILE --- */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		status.SetText("Status: Menyiapkan...")
		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		go func() {
			exec.Command("su", "-c", "rm -f "+target).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()
			status.SetText("Status: Berjalan")
			updateKernelStatus()
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", target)
			} else {
				cmd = exec.Command("su", "-c", "sh "+target)
			}
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			stdin, _ = cmd.StdinPipe()
			cmd.Stdout = term
			cmd.Stderr = term
			err := cmd.Run()
			if err != nil {
				term.Write([]byte(fmt.Sprintf("\n\x1b[31m[EXIT ERROR: %v]\x1b[0m\n", err)))
			} else {
				term.Write([]byte("\n\x1b[32m[Selesai]\x1b[0m\n"))
			}
			status.SetText("Status: Selesai")
			stdin = nil
			time.Sleep(1 * time.Second)
			updateKernelStatus()
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

	/* ==========================================
	   UI CONSTRUCTION
	========================================== */

	// Header
	titleText := canvas.NewText("ROOT EXECUTOR", theme.ForegroundColor())
	titleText.TextSize = 16
	titleText.TextStyle = fyne.TextStyle{Bold: true}

	headerLeft := container.NewVBox(
		titleText,
		kernelLabel,
	)

	installBtn := widget.NewButtonWithIcon("Install Driver", theme.DownloadIcon(), func() {
		dialog.ShowConfirm("Auto Install", "Download dan install driver sesuai kernel HP?", func(ok bool) {
			if ok {
				autoInstallKernel()
			}
		}, w)
	})
	
	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() {
		term.Clear()
	})

	headerRight := container.NewHBox(installBtn, clearBtn)
	headerBar := container.NewBorder(nil, nil, 
		container.NewPadded(headerLeft), 
		headerRight,
	)
	topSection := container.NewVBox(
		headerBar,
		container.NewPadded(status),
		widget.NewSeparator(),
	)

	// Footer
	copyright := canvas.NewText("Made by TANGSAN", color.RGBA{R: 255, G: 0, B: 128, A: 255})
	copyright.TextSize = 10
	copyright.Alignment = fyne.TextAlignCenter
	copyright.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}

	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)
	inputContainer := container.NewPadded(
		container.NewBorder(nil, nil, nil, sendBtn, input),
	)
	bottomSection := container.NewVBox(
		copyright,
		inputContainer,
	)

	// Main Layout
	mainLayer := container.NewBorder(
		topSection,
		bottomSection,
		nil, nil,
		term.scroll,
	)

	// FAB
	fabBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) {
			if r != nil {
				runFile(r)
			}
		}, w).Show()
	})
	fabBtn.Importance = widget.HighImportance

	fabContainer := container.NewVBox(
		layout.NewSpacer(), 
		container.NewHBox(
			layout.NewSpacer(),
			fabBtn,
			widget.NewLabel(" "), 
		),
		widget.NewLabel("      "), 
		widget.NewLabel("      "), 
	)

	finalLayout := container.NewStack(mainLayer, fabContainer)

	w.SetContent(finalLayout)
	w.ShowAndRun()
}

