package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// customBuffer menggunakan RichText agar tajam dan mendukung segmen teks
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	window   fyne.Window
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	// Menghapus kode ANSI agar tidak muncul teks mentah seperti [1;36m
	reControl := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	cleanText := reControl.ReplaceAllString(string(p), "")

	// Manipulasi visual Expires
	reExp := regexp.MustCompile(`(?i)Expires:.*`)
	finalText := reExp.ReplaceAllString(cleanText, "Expires: 99 days")

	// Menambahkan teks ke RichText secara tajam
	cb.richText.Segments = append(cb.richText.Segments, &widget.TextSegment{
		Text: finalText,
		Style: widget.RichTextStyleInline,
	})
	
	cb.richText.Refresh()
	cb.scroll.ScrollToBottom() // Otomatis scroll ke bawah
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Final")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Menggunakan RichText: Tajam, Anti-Keyboard, dan Ringan
	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan...")
		outputRich.Segments = nil // Bersihkan log lama

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF")) // Cek jika file SHC Binary

		go func() {
			// Salin file ke folder internal yang aman
			exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath).Run()
			
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin", "TERM=dumb")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{richText: outputRich, scroll: logScroll, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			statusLabel.SetText("Status: Berjalan...")
			cmd.Run()
			statusLabel.SetText("Status: Selesai")
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
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

