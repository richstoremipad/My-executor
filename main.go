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

// customBuffer menggunakan widget.Label untuk tampilan lebih tajam
type customBuffer struct {
	label  *widget.Label
	scroll *container.Scroll
	window fyne.Window
}

func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	cleanText := cleanAnsi(string(p))
	re := regexp.MustCompile(`(?i)Expires:.*`)
	modifiedText := re.ReplaceAllString(cleanText, "Expires: 99 days")

	// Update teks pada Label
	cb.label.SetText(cb.label.Text + modifiedText)
	
	// Otomatis scroll ke paling bawah setiap ada log baru
	cb.scroll.ScrollToBottom()
	cb.window.Canvas().Refresh(cb.label)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor (Sharp Text)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// MENGGUNAKAN LABEL: Teks tetap putih tajam dan TIDAK memicu keyboard
	outputLabel := widget.NewLabel("")
	outputLabel.TextStyle = fyne.TextStyle{Monospace: true}
	outputLabel.Wrapping = fyne.TextWrapBreak

	// Scroll container agar bisa digeser dengan jari
	logScroll := container.NewScroll(outputLabel)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputLabel.SetText("") // Reset log

		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "temp_bin")
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()
		os.Chmod(internalPath, 0755)

		go func() {
			statusLabel.SetText("Status: Berjalan (Interaktif)...")
			cmdString := fmt.Sprintf("cd %s && ./%s", filepath.Dir(internalPath), filepath.Base(internalPath))
			cmd := exec.Command("su", "-c", cmdString)
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin", "TERM=dumb")

			stdinPipe, _ = cmd.StdinPipe()
			
			// Hubungkan ke label dan scroll
			combinedBuf := &customBuffer{label: outputLabel, scroll: logScroll, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			outputLabel.SetText(outputLabel.Text + "> " + inputEntry.Text + "\n")
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })

	btnSelect := widget.NewButton("Pilih File (Bash/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputLabel.SetText("")
	})

	inputContainer := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	controls := container.NewVBox(btnSelect, btnClear, statusLabel)

	myWindow.SetContent(container.NewBorder(
		controls,
		inputContainer, 
		nil, nil, 
		logScroll,
	))

	myWindow.ShowAndRun()
}

