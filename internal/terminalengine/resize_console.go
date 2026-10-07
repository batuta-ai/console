package terminalengine

// ResizeChecked resizes the screen and saved DEC 1049 cursor state together.
// Call this method when a Parser is attached to the screen.
func (p *Parser) ResizeChecked(rows, cols int) error {
	mainTrim, err := p.screen.resizeChecked(rows, cols)
	if err != nil {
		return err
	}
	if p.dec1049CursorSaved {
		p.dec1049SavedCursor.cursor.Row -= mainTrim
		p.dec1049SavedCursor.cursor = clampCursorToSize(p.dec1049SavedCursor.cursor, rows, cols)
	}
	return nil
}
