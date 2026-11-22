package history

import "bytes"

var turnReplayLineBreaks = bytes.Repeat([]byte{'\n'}, 256)

// turnReplayWriter streams a semantic rebuild into prepared rows. Delay only
// its trailing CR/LF, matching the previous whole-string TrimRight behavior.
type turnReplayWriter struct {
	historyPageWriter
	newlines int
	carriage bool
	err      error
}

func (w *turnReplayWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	content := bytes.TrimRight(data, "\r\n")
	if len(content) > 0 {
		for w.newlines > 0 {
			count := min(w.newlines, len(turnReplayLineBreaks))
			if _, w.err = w.historyPageWriter.Write(turnReplayLineBreaks[:count]); w.err != nil {
				return 0, w.err
			}
			w.newlines -= count
		}
		if w.carriage {
			if _, w.err = w.historyPageWriter.Write([]byte{'\r'}); w.err != nil {
				return 0, w.err
			}
		}
		w.carriage = false
		if n, err := w.historyPageWriter.Write(content); err != nil {
			w.err = err
			return n, err
		}
	}
	tail := data[len(content):]
	w.newlines += bytes.Count(tail, []byte{'\n'})
	w.carriage = w.carriage || bytes.IndexByte(tail, '\r') >= 0
	return len(data), nil
}
