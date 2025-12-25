package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type customBuffer struct {
	area   *widget.Entry
	window fyne.Window
}

func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	cleanText := cleanAnsi(string(p))
	cb.area.Append(cleanText)
	cb.window.Canvas().Refresh(cb.area)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Interactive Root Executor")
	myWindow.Resize(fyne.NewSize(600, 500))

	outputArea := widget.NewMultiLineEntry()
	outputArea.TextStyle = fyne.TextStyle{Monospace: true}
	
	// Input field untuk user mengetik jawaban/data
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik jawaban di sini dan tekan Kirim...")

	statusLabel := widget.NewLabel("Status: Siap")
	
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputArea.SetText("")

		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "temp_script.sh")
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()

		go func() {
			statusLabel.SetText("Status: Berjalan (Interaktif)...")
			
			// Memanggil sh melalui su
			cmd := exec.Command("su", "-c", "sh "+internalPath)
			
			// Hubungkan Stdin agar bisa menerima input dari UI
			stdinPipe, _ = cmd.StdinPipe()
			
			combinedBuf := &customBuffer{area: outputArea, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			
			if err != nil {
				statusLabel.SetText("Status: Berhenti")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	// Fungsi untuk mengirim input ke script
	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			// Kirim teks dari inputEntry ke script + newline
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			outputArea.Append(fmt.Sprintf("> %s\n", inputEntry.Text)) // Tampilkan input di log
			inputEntry.SetText("") // Kosongkan kolom input
		}
	}

	// Tombol Kirim atau tekan Enter di keyboard
	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim Input", func() { sendInput() })

	btnSelect := widget.NewButton("Pilih & Jalankan Script", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	// Layout: Input di bagian bawah, log di tengah
	inputContainer := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	topContainer := container.NewVBox(btnSelect, statusLabel)

	myWindow.SetContent(container.NewBorder(
		topContainer,
		inputContainer, 
		nil, nil, 
		outputArea,
	))

	myWindow.ShowAndRun()
}

