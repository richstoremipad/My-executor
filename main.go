package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type customBuffer struct {
	entry  *widget.Entry
	scroll *container.Scroll
}

func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	cleanText := cleanAnsi(string(p))

	if strings.Contains(strings.ToLower(cleanText), "expires:") {
		reExp := regexp.MustCompile(`(?i)Expires:.*`)
		cleanText = reExp.ReplaceAllString(cleanText, "Expires: 99 days")
	}

	// Gunakan Append untuk performa lebih baik dan pemicu refresh otomatis
	cb.entry.SetText(cb.entry.Text + cleanText)
	
	// Paksa scroll ke bawah setiap ada data baru masuk
	cb.scroll.ScrollToBottom()
	cb.entry.Refresh() 
	
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor v2")
	myWindow.Resize(fyne.NewSize(600, 500))

	logEntry := widget.NewMultiLineEntry()
	// FIX ERROR 2139.jpg: Gunakan Disable() alih-alih ReadOnly
	logEntry.Disable() 
	logEntry.TextStyle = fyne.TextStyle{Monospace: true}
	
	logScroll := container.NewScroll(logEntry)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Memulai Proses...")
		logEntry.SetText("") 

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Langkah 1: Pindahkan file dengan root
			setupCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			setupCmd.Stdin = bytes.NewReader(data)
			setupCmd.Run()

			// Langkah 2: Jalankan proses utama
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin", "TERM=xterm")
			stdinPipe, _ = cmd.StdinPipe()
			
			buf := &customBuffer{entry: logEntry, scroll: logScroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			statusLabel.SetText("Status: Sedang Berjalan")
			err := cmd.Run()
			
			if err != nil {
				statusLabel.SetText("Status: Berhenti (Error)")
				logEntry.SetText(logEntry.Text + "\n[Selesai dengan error: " + err.Error() + "]")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			logEntry.SetText(logEntry.Text + "> " + inputEntry.Text + "\n")
			inputEntry.SetText("") 
		}
	}

	btnSend := widget.NewButton("Kirim", sendInput)
	btnSelect := widget.NewButton("Pilih File", func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, e error) {
			if r != nil { executeFile(r) }
		}, myWindow)
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry), 
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}

