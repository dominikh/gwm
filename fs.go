// +build ignore

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"honnef.co/go/gwm/menu"
	"honnef.co/go/spew"

	"github.com/BurntSushi/xgbutil/xprop"
	p9p "github.com/docker/go-p9p"
	"golang.org/x/net/context"
)

const (
	qidRoot = iota + 1
	qidMenu
	qidMenuItems
	qidMenuSelection
	qidMenuShow
	qidLast
)

var _ Directory = (*FSMenu)(nil)

type FSMenu struct {
	wm     *WM
	parent Directory

	items     []string
	selection string
}

func (menu *FSMenu) Name() string {
	return "menu"
}

func (menu *FSMenu) Qid() uint64 {
	return qidMenu
}

func (menu *FSMenu) Parent() Directory {
	return menu.parent
}

func (menu *FSMenu) Files() []File {
	return []File{
		fsMenuItems{menu},
		fsMenuSelection{menu},
		fsMenuShow{menu},
	}
}

type fsMenuItems struct {
	menu *FSMenu
}

func (items fsMenuItems) Parent() Directory {
	return items.menu
}

func (items fsMenuItems) Qid() uint64 {
	return qidMenuItems
}

func (items fsMenuItems) Name() string {
	return "items"
}

func (items fsMenuItems) Write(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	ins := bytes.Split(b, []byte{'\n'})
	for _, input := range ins[:len(ins)-1] {
		items.menu.items = append(items.menu.items, string(input))
	}
	return nil
}

type fsMenuSelection struct {
	menu *FSMenu
}

func (sel fsMenuSelection) Parent() Directory {
	return sel.menu
}

func (sel fsMenuSelection) Qid() uint64 {
	return qidMenuSelection
}

func (sel fsMenuSelection) Name() string {
	return "selection"
}

func (sel fsMenuSelection) Read() []byte {
	return []byte(sel.menu.selection)
}

type fsMenuShow struct {
	menu *FSMenu
}

func (show fsMenuShow) Parent() Directory {
	return show.menu
}

func (show fsMenuShow) Qid() uint64 {
	return qidMenuShow
}

func (show fsMenuShow) Name() string {
	return "show"
}

func (show fsMenuShow) Write(b []byte) error {
	var items []menu.Entry
	for _, item := range show.menu.items {
		items = append(items, menu.Entry{
			Display: item,
			Payload: item,
		})
	}
	spew.Dump(items)
	m, err := show.menu.wm.newMenu("generic", items, menu.FilterPrefix)
	if err != nil {
		return err
	}
	m.Show()
	ret, ok := m.Wait()
	if !ok {
		show.menu.selection = ""
	} else {
		show.menu.selection = ret.Payload.(string)
	}
	show.menu.items = nil
	return nil
}

type Directory interface {
	File
	Parent() Directory
	Files() []File
}

type File interface {
	Name() string
	Qid() uint64
}

type Remover interface {
	Remove()
}

type Reader interface {
	Read() []byte
}

type Writer interface {
	Write([]byte) error
}

type FSDirectory struct {
	parent Directory
	name   string
	files  []File
}

func (dir FSDirectory) Parent() Directory {
	return dir.parent
}

func (dir FSDirectory) Name() string {
	return dir.name
}

func (dir FSDirectory) Qid() uint64 {
	// XXX
	var n uint64
	for _, b := range dir.name {
		n += uint64(b)
	}
	return n
}

func (dir FSDirectory) Files() []File {
	return dir.files
}

type FSWindow struct {
	parent Directory
	name   string
	win    *Window
}

func (win FSWindow) Parent() Directory {
	return win.parent
}

var _ File = FSWindow{}

type FSWindowAttr struct {
	win     *Window
	name    string
	readFn  func() []byte
	writeFn func([]byte) error
}

func (attr FSWindowAttr) Qid() uint64 {
	// XXX
	return uint64(attr.win.Id)
}

func (attr FSWindowAttr) Name() string {
	return attr.name
}

func (attr FSWindowAttr) Read() []byte {
	if attr.readFn == nil {
		return nil
	}
	b := attr.readFn()
	b = append(b, '\n')
	return b
}

func (attr FSWindowAttr) Write(b []byte) error {
	if attr.writeFn == nil {
		return p9p.ErrNowrite
	}
	return attr.writeFn(b)
}

func (win FSWindow) Files() []File {
	return []File{
		FSWindowAttr{
			win.win,
			"name",
			func() []byte { return []byte(win.win.Name()) },
			func(b []byte) error { win.win.SetName(string(b)); return nil },
		},
		FSWindowAttr{
			win.win,
			"size",
			func() []byte {
				s := fmt.Sprintf("%d %d",
					win.win.Layout.Geometry.Width,
					win.win.Layout.Geometry.Height)
				return []byte(s)
			},
			func(b []byte) error {
				if len(b) < 3 {
					return p9p.ErrNowrite
				}
				if b[len(b)-1] == '\n' {
					b = b[:len(b)-1]
				}
				parts := bytes.Split(b, []byte{' '})
				if len(parts) != 2 {
					return p9p.ErrNowrite
				}
				i1, err1 := strconv.Atoi(string(parts[0]))
				i2, err2 := strconv.Atoi(string(parts[1]))
				if err1 != nil || err2 != nil {
					return p9p.ErrNowrite
				}
				win.win.Resize(i1, i2)
				return nil
			},
		},
		FSWindowAttr{
			win.win,
			"id",
			func() []byte { return []byte(fmt.Sprintf("%d", win.win.Id)) },
			nil,
		},
		FSWindowAttr{
			win.win,
			"pid",
			func() []byte {
				raw, err := xprop.GetProperty(win.win.wm.X, win.win.Id, "_NET_WM_PID")
				n, err := xprop.PropValNum(raw, err)
				if err != nil {
					return nil
				}
				return []byte(fmt.Sprintf("%d", n))
			},
			nil,
		},
		FSWindowAttr{
			win.win,
			"last_activity",
			func() []byte {
				raw, err := xprop.GetProperty(win.win.wm.X, win.win.Id, "_NET_WM_USER_TIME")
				log.Println(raw, err)
				n, err := xprop.PropValNum(raw, err)
				if err != nil {
					return nil
				}
				return []byte(fmt.Sprintf("%d", n))
			},
			nil,
		},
	}
}

func (win FSWindow) Name() string {
	if win.name != "" {
		return win.name
	}
	return fmt.Sprintf("%d", win.win.Id)
}

func (win FSWindow) Qid() uint64 {
	return qidLast + uint64(win.win.Id)
}

func (win FSWindow) Remove() {
	win.win.Kill()
}

type Root struct {
	wm *WM
}

func (r Root) Parent() Directory {
	return r
}

// FIXME: the use of fsmenu is racy
var fsmenu = &FSMenu{}

func (r Root) Files() []File {
	log.Println("-> Generating root files")
	wins := FSDirectory{
		parent: r,
		name:   "wins",
	}

	for _, win := range r.wm.Windows {
		wins.files = append(wins.files, FSWindow{
			parent: wins,
			win:    win,
		})
	}
	if r.wm.CurWindow != nil {
		wins.files = append(wins.files, FSWindow{
			parent: wins,
			win:    r.wm.CurWindow,
			name:   "sel",
		})
	}
	nameGroups := &FSWindowNameGroup{
		parent: wins,
		name:   "by-name",
		wm:     r.wm,
	}
	wins.files = append(wins.files, nameGroups)
	fsmenu.wm = r.wm
	fsmenu.parent = r
	return []File{
		wins,
		fsmenu,
	}
}

type FSWindowNameGroup struct {
	parent Directory
	name   string
	wm     *WM

	files []File
}

func (g *FSWindowNameGroup) Qid() uint64 {
	// XXX
	return 321
}

func (g *FSWindowNameGroup) Parent() Directory {
	return g.parent
}

func (g *FSWindowNameGroup) Name() string {
	return g.name
}

func (g *FSWindowNameGroup) Files() []File {
	if g.files != nil {
		return g.files
	}
	log.Println("-> Generating by-name files")
	m := map[string][]*Window{}
	for _, win := range g.wm.Windows {
		name := win.Name()
		if name == "" {
			continue
		}
		m[name] = append(m[name], win)
	}

	var out []File
	for name, wins := range m {
		name = strings.Replace(name, "/", "_", -1)
		dir := FSDirectory{
			parent: g,
			name:   name,
		}
		for _, win := range wins {
			dir.files = append(dir.files, FSWindow{parent: dir, win: win})
		}
		out = append(out, dir)
	}

	g.files = out
	return out
}

func (Root) Qid() uint64 {
	return qidRoot
}

func (Root) Name() string { return "/" }

type session struct {
	wm      *WM
	fids    map[p9p.Fid]File
	readers map[p9p.Fid]io.ReaderAt
}

func newSession(wm *WM) session {
	return session{wm, map[p9p.Fid]File{}, map[p9p.Fid]io.ReaderAt{}}
}

func (session) Auth(ctx context.Context, afid p9p.Fid, uname string, aname string) (p9p.Qid, error) {
	panic("not implemented")
}

func (s session) Attach(ctx context.Context, fid p9p.Fid, afid p9p.Fid, uname string, aname string) (p9p.Qid, error) {
	log.Printf("attach %d", fid)
	s.fids[fid] = Root{s.wm}
	return p9p.Qid{
		Type:    p9p.QTDIR,
		Version: 0,
		Path:    qidRoot,
	}, nil
}

func (s session) Clunk(ctx context.Context, fid p9p.Fid) error {
	log.Printf("clunk %d", fid)
	delete(s.fids, fid)
	delete(s.readers, fid)
	return nil
}

func (s session) Remove(ctx context.Context, fid p9p.Fid) error {
	file, ok := s.fids[fid].(Remover)
	if !ok {
		return p9p.ErrNoremove
	}
	file.Remove()
	return nil
}

func (s session) Walk(ctx context.Context, fid p9p.Fid, newfid p9p.Fid, names ...string) ([]p9p.Qid, error) {
	log.Printf("walk %d -> %d: %s", fid, newfid, strings.Join(names, "/"))
	node := s.fids[fid]

	var qids []p9p.Qid
outer:
	for _, name := range names {
		dir, ok := node.(Directory)
		if !ok {
			if len(qids) == 0 {
				return nil, p9p.ErrWalknodir
			}
			return qids, nil
		}
		if name == ".." {
			node = dir.Parent()
			qids = append(qids, qid(node))
			continue outer
		}
		files := dir.Files()
		for _, file := range files {
			if file.Name() == name {
				node = file

				qids = append(qids, qid(file))
				continue outer
			}
		}
		if len(qids) == 0 {
			return nil, p9p.ErrNotfound
		}
		return qids, nil
	}
	s.fids[newfid] = node
	return qids, nil
}

func qid(file File) p9p.Qid {
	typ := p9p.QType(p9p.QTFILE)
	if _, isDir := file.(Directory); isDir {
		typ = p9p.QTDIR
	}
	return p9p.Qid{
		Type:    typ,
		Version: 0,
		Path:    file.Qid(),
	}
}

func (s session) Read(ctx context.Context, fid p9p.Fid, p []byte, offset int64) (n int, err error) {
	defer func() {
		log.Printf("read = %d, %v", n, err)
	}()
	log.Printf("read %d at %d into buffer of size %d", fid, offset, len(p))
	r, ok := s.readers[fid]
	if !ok {
		return 0, errors.New("reading prohibited")
	}
	n, err = r.ReadAt(p, offset)
	if err == io.EOF {
		err = nil
	}
	return n, err
}

func (s session) Write(ctx context.Context, fid p9p.Fid, p []byte, offset int64) (n int, err error) {
	if offset != 0 {
		return 0, p9p.ErrBadoffset
	}
	w, ok := s.fids[fid].(Writer)
	if !ok {
		return 0, p9p.ErrNowrite
	}
	if err := w.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s session) Open(ctx context.Context, fid p9p.Fid, mode p9p.Flag) (p9p.Qid, uint32, error) {
	log.Printf("open(%d, %d)", fid, mode)
	file, ok := s.fids[fid]
	if !ok {
		return p9p.Qid{}, 0, p9p.ErrNotfound
	}
	var data []byte
	switch file := file.(type) {
	case Directory:
		buf := &bytes.Buffer{}
		for _, file := range file.Files() {

			dir := p9p.Dir{
				Qid:        qid(file),
				Mode:       fileMode(file),
				AccessTime: time.Now(),
				ModTime:    time.Now(),
				Name:       file.Name(),
				UID:        "dominikh",
				GID:        "dominikh",
				MUID:       "dominikh",
			}
			_ = p9p.EncodeDir(p9p.NewCodec(), buf, &dir)
		}
		data = buf.Bytes()
	case Reader:
		data = file.Read()
	default:
		return qid(file), 0, nil
	}
	s.readers[fid] = bytes.NewReader(data)
	return qid(file), 0, nil
}

func (session) Create(ctx context.Context, parent p9p.Fid, name string, perm uint32, mode p9p.Flag) (p9p.Qid, uint32, error) {
	panic("not implemented")
}

func fileMode(f File) uint32 {
	mode := p9p.DMREAD
	if _, isDir := f.(Directory); isDir {
		mode |= p9p.DMDIR | p9p.DMEXEC
	}
	if _, isWriter := f.(Writer); isWriter {
		mode |= p9p.DMWRITE
	}
	return uint32(mode)
}

func (s session) Stat(ctx context.Context, fid p9p.Fid) (p9p.Dir, error) {
	log.Printf("stat %d", fid)
	// TODO check fid
	file := s.fids[fid]
	log.Println(qid(file))
	return p9p.Dir{
		Qid:        qid(file),
		Mode:       fileMode(file),
		AccessTime: time.Now(),
		ModTime:    time.Now(),
		Name:       file.Name(),
		UID:        "dominikh",
		GID:        "dominikh",
		MUID:       "dominikh",
	}, nil
}

func (session) WStat(ctx context.Context, fid p9p.Fid, dir p9p.Dir) error {
	return nil
}

func (session) Version() (msize int, version string) {
	panic("not implemented")
}
