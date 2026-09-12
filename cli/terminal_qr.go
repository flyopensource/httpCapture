package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/term"
)

const (
	terminalQRAuto   = "auto"
	terminalQRAlways = "always"
	terminalQRNever  = "never"
)

func validateTerminalQRMode(mode string) error {
	switch mode {
	case terminalQRAuto, terminalQRAlways, terminalQRNever:
		return nil
	default:
		return errors.New("--terminal-qr 必须是 auto、always 或 never")
	}
}

func compactTerminalQRCode(code *qrcode.QRCode) (text string, columns int, rows int) {
	text = code.ToSmallString(false)
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		columns = max(columns, utf8.RuneCountInString(line))
		rows++
	}
	return text, columns, rows
}

func shouldPrintTerminalQRCode(mode string, interactive bool, availableColumns, availableRows, requiredColumns, requiredRows int) bool {
	switch mode {
	case terminalQRAlways:
		return true
	case terminalQRNever:
		return false
	default:
		// Keep one spare column and row so terminal auto-wrap and the trailing
		// newline cannot push part of the QR code out of the viewport.
		return interactive && requiredColumns < availableColumns && requiredRows < availableRows
	}
}

func printTerminalQRCode(output *os.File, code *qrcode.QRCode, mode string) error {
	text, requiredColumns, requiredRows := compactTerminalQRCode(code)
	fd := int(output.Fd())
	interactive := term.IsTerminal(fd)
	availableColumns, availableRows, sizeErr := term.GetSize(fd)
	if sizeErr != nil {
		availableColumns, availableRows = 0, 0
	}

	if shouldPrintTerminalQRCode(mode, interactive, availableColumns, availableRows, requiredColumns, requiredRows) {
		_, err := fmt.Fprint(output, text)
		return err
	}

	switch mode {
	case terminalQRNever:
		fmt.Fprintln(output, "终端二维码: 已关闭；请打开上面的 PNG")
	case terminalQRAuto:
		if interactive && sizeErr == nil {
			fmt.Fprintf(output, "终端二维码: 已省略（需要至少 %d×%d 字符，当前 %d×%d）；请打开上面的 PNG，或使用 --terminal-qr always 强制显示\n", requiredColumns+1, requiredRows+1, availableColumns, availableRows)
		} else {
			fmt.Fprintf(output, "终端二维码: 已省略（需要至少 %d×%d 字符，未检测到交互式终端）；请打开上面的 PNG，或使用 --terminal-qr always 强制显示\n", requiredColumns+1, requiredRows+1)
		}
	}
	return nil
}
