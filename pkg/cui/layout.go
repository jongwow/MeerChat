package cui

import (
	"fmt"
	"github.com/jroimartin/gocui"
)

func layout(g *gocui.Gui) error {
	maxX, maxY := 80, 30
	if v, err := g.SetView("room", 0, 0, int(0.2*float32(maxX)), maxY-11); err != nil {
		if err != gocui.ErrUnknownView {
			return err
		}
		v.Title = "Room"
		v.Wrap = true
	}

	if v, err := g.SetView("UserInfo", 0, maxY-10, int(0.2*float32(maxX)), maxY-6); err != nil {
		if err != gocui.ErrUnknownView {
			return err
		}
		v.Title = "UserInfo"
		v.Wrap = true
		name, ip, port := MeerNode.User.GetUserInfo()
		info := fmt.Sprintf("%s\n%s\n:%s", name, ip, port)
		v.Write([]byte(info))
	}

	if v, err := g.SetView("chat", int(0.2*float32(maxX))+1, 0, maxX-1, maxY-11); err != nil {
		if err != gocui.ErrUnknownView {
			return err
		}
		v.Title = "Chat"
		v.Wrap = true
		v.Autoscroll = true
	}

	if v, err := g.SetView("chatline", int(0.2*float32(maxX))+1, maxY-10, maxX-1, maxY-6); err != nil {
		if err != gocui.ErrUnknownView {
			return err
		}
		v.Title = "Chatline(ctrl+a)"
		v.Wrap = true
		v.Autoscroll = true
		v.Editable = true
	}

	if v, err := g.SetView("cmdline", 0, maxY-5, maxX-1, maxY-1); err != nil {
		if err != gocui.ErrUnknownView {
			return err
		}
		v.Title = "Cmdline"
		v.Wrap = true
		v.Autoscroll = true
		v.Editable = true

		if err = newLine(g, v); err != nil {
			return err
		}

		if _, err = setCurrentViewOnTop(g, "cmdline"); err != nil {
			return err
		}
	}

	return nil
}