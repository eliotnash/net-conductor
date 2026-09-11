package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"strings"
	"unsafe"
)

type desktopInput struct {
	Kind   string  `json:"kind"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button int     `json:"button"`
	Down   bool    `json:"down"`
	Code   string  `json:"code"`
	Delta  int32   `json:"delta"`
}
type winInput struct {
	Type uint32
	Pad  uint32
	Data [32]byte
}

func inputMain() {
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendInput")
	pressed := map[uint16]bool{}
	buttons := map[int]bool{}
	send := func(i winInput) error {
		n, _, _ := proc.Call(1, uintptr(unsafe.Pointer(&i)), unsafe.Sizeof(i))
		if n != 1 {
			return errors.New("input blocked by Windows desktop permissions")
		}
		return nil
	}
	key := func(vk uint16, down bool) error {
		var i winInput
		i.Type = 1
		binary.LittleEndian.PutUint16(i.Data[:2], vk)
		if !down {
			binary.LittleEndian.PutUint32(i.Data[4:8], 2)
		}
		return send(i)
	}
	release := func() {
		for button := range buttons {
			var i winInput
			flag := uint32(4)
			if button == 2 {
				flag = 16
			}
			binary.LittleEndian.PutUint32(i.Data[12:16], flag)
			send(i)
			delete(buttons, button)
		}
		for vk := range pressed {
			key(vk, false)
			delete(pressed, vk)
		}
	}
	defer release()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4096)
	for scanner.Scan() {
		var r desktopInput
		err := json.Unmarshal(scanner.Bytes(), &r)
		if err == nil {
			switch r.Kind {
			case "release":
				release()
			case "key":
				vk := virtualKey(r.Code)
				if vk == 0 {
					err = errors.New("unsupported key")
				} else {
					err = key(vk, r.Down)
					if r.Down {
						pressed[vk] = true
					} else {
						delete(pressed, vk)
					}
				}
			case "move", "button", "wheel":
				var i winInput
				if r.Kind == "move" {
					if r.X < 0 || r.X > 1 || r.Y < 0 || r.Y > 1 {
						err = errors.New("invalid pointer")
						break
					}
					binary.LittleEndian.PutUint32(i.Data[:4], uint32(r.X*65535))
					binary.LittleEndian.PutUint32(i.Data[4:8], uint32(r.Y*65535))
					binary.LittleEndian.PutUint32(i.Data[12:16], 0x8001)
				}
				if r.Kind == "button" {
					if r.Down {
						buttons[r.Button] = true
					} else {
						delete(buttons, r.Button)
					}
					flags := uint32(0)
					if r.Button == 0 {
						flags = 2
					} else if r.Button == 2 {
						flags = 8
					} else {
						err = errors.New("unsupported button")
						break
					}
					if !r.Down {
						flags *= 2
					}
					binary.LittleEndian.PutUint32(i.Data[12:16], flags)
				}
				if r.Kind == "wheel" {
					if r.Delta < -1200 || r.Delta > 1200 {
						err = errors.New("invalid wheel")
						break
					}
					binary.LittleEndian.PutUint32(i.Data[8:12], uint32(r.Delta))
					binary.LittleEndian.PutUint32(i.Data[12:16], 0x800)
				}
				err = send(i)
			default:
				err = errors.New("invalid input")
			}
		}
		result := map[string]any{"ok": err == nil}
		if err != nil {
			result["error"] = err.Error()
		}
		b, _ := json.Marshal(result)
		fmt.Println(string(b))
	}
}
func virtualKey(code string) uint16 {
	if len(code) == 4 && strings.HasPrefix(code, "Key") && code[3] >= 'A' && code[3] <= 'Z' {
		return uint16(code[3])
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") && code[5] >= '0' && code[5] <= '9' {
		return uint16(code[5])
	}
	return map[string]uint16{"Enter": 13, "Escape": 27, "Backspace": 8, "Tab": 9, "Space": 32, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Delete": 46, "Home": 36, "End": 35, "PageUp": 33, "PageDown": 34, "ShiftLeft": 16, "ShiftRight": 16, "ControlLeft": 17, "ControlRight": 17, "AltLeft": 18, "AltRight": 18, "MetaLeft": 91, "MetaRight": 92, "CapsLock": 20, "Minus": 189, "Equal": 187, "BracketLeft": 219, "BracketRight": 221, "Backslash": 220, "Semicolon": 186, "Quote": 222, "Comma": 188, "Period": 190, "Slash": 191, "Backquote": 192, "F1": 112, "F2": 113, "F3": 114, "F4": 115, "F5": 116, "F6": 117, "F7": 118, "F8": 119, "F9": 120, "F10": 121, "F11": 122, "F12": 123}[code]
}
