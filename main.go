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

// customBuffer untuk menangani penulisan log secara real-time
type customBuffer struct {
	entry  *widget.Entry
	scroll *container.Scroll
}

func cleanAnsi(str string) string {
	// Menghapus kode warna ANSI agar log bersih dan tidak blank
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	rawText := string(p)
	cleanText := cleanAnsi(rawText)

	// Logika manipulasi teks Expires
	if strings.Contains(strings.ToLower(cleanText), "expires:") {
		reExp := regexp.MustCompile(`(?i)Expires:.*`)
		cleanText = reExp.ReplaceAllString(cleanText, "Expires: 99 days")
	}

	// Menambahkan teks ke Entry dan scroll ke bawah secara otomatis
	cb.entry.SetText(cb.entry.Text + cleanText)
	cb.scroll.ScrollToBottom()
	
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Root Executor")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Menggunakan Entry MultiLine agar lebih stabil menampilkan log daripada RichText
	logEntry := widget.NewMultiLineEntry()
	logEntry.ReadOnly = true
	logEntry.TextStyle = fyne.TextStyle{Monospace: true}
	logScroll := container.NewScroll(logEntry)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Berjalan...")
		logEntry.SetText("") // Bersihkan log lama

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Salin file ke folder tmp dan beri izin eksekusi
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			// Set environment terminal agar script merasa berjalan di shell normal
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/data/local/tmp", "TERM=xterm")

			stdinPipe, _ = cmd.StdinPipe()
			
			// Inisialisasi buffer output
			buf := &customBuffer{entry: logEntry, scroll: logScroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			err := cmd.Run()
			
			// Gunakan refresh sederhana tanpa Driver() untuk menghindari error 2136.jpg
			if err != nil {
				statusLabel.SetText("Status: Selesai dengan Error")
				logEntry.SetText(logEntry.Text + "\n[Proses Berhenti: " + err.Error() + "]")
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
	btnSelect := widget.NewButton("Pilih File (Bash/SHC Binary)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry), 
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}

