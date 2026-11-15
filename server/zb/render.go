package zb

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
)

func escapeHTML(value string) string { return html.EscapeString(value) }

var (
	reInlineCode  = regexp.MustCompile("`([^`]+)`")
	reInlineImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	reInlineLink  = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reInlineBold  = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reHeading     = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reHR          = regexp.MustCompile(`^(-{3,}|\*{3,})$`)
	reListItem    = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
)

const docStyle = `body{margin:0;padding:32px 40px;background:#fff;color:#222;font:15px/1.75 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;max-width:860px} h1,h2,h3{line-height:1.3;margin:1.4em 0 .6em} h1{font-size:26px;border-bottom:1px solid #e5e5e5;padding-bottom:.3em} h2{font-size:21px} h3{font-size:17px} a{color:#185fa5} img{max-width:100%} code{background:#f4f4f2;padding:2px 5px;border-radius:4px;font-size:13px} pre{background:#f6f6f4;padding:14px;border-radius:8px;overflow:auto} pre code{background:none;padding:0} blockquote{border-left:3px solid #d0d0d0;margin:1em 0;padding:.2em 1em;color:#555} hr{border:0;border-top:1px solid #e5e5e5} table{border-collapse:collapse} td,th{border:1px solid #ddd;padding:6px 10px}`

const dirStyle = `body{margin:0;padding:28px 32px;background:#fff;color:#1f2329;font:14px/1.7 -apple-system,BlinkMacSystemFont,'Segoe UI','Microsoft YaHei',sans-serif;max-width:1100px} h1{font-size:20px;margin:0 0 4px} .hint{color:#777;font-size:13px;margin:0 0 16px} .switch{display:flex;gap:6px;margin:0 0 18px} .switch a{font-size:12px;padding:4px 12px;border:1px solid #e4e7ec;border-radius:6px;color:#6b7280;text-decoration:none} .switch a.on{border-color:#185fa5;background:#e6f1fb;color:#185fa5} ul{list-style:none;padding:0;margin:0} li{display:flex;justify-content:space-between;gap:16px;padding:7px 10px;border-bottom:1px solid #f0f0f0} li:hover{background:#f7f9fc} li a{color:#185fa5;text-decoration:none} li a:hover{text-decoration:underline} li span{color:#888;font-size:12px;white-space:nowrap} .grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:12px} .card{display:block;border:1px solid #e4e7ec;border-radius:8px;overflow:hidden;color:inherit;text-decoration:none} .card:hover{border-color:#185fa5} .card img{display:block;width:100%;height:120px;object-fit:cover;background:#f7f8fa} .card .n{display:block;padding:7px 9px 0;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis} .card .s{display:block;padding:0 9px 8px;font-size:11px;color:#9aa1ac} .folders{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 16px} .folders a{font-size:12px;padding:4px 10px;background:#f7f8fa;border:1px solid #e4e7ec;border-radius:99px;color:#185fa5;text-decoration:none} .empty{padding:28px;text-align:center;color:#9aa1ac;border:1px dashed #e4e7ec;border-radius:8px}`

const viewerStyle = `*{box-sizing:border-box}
html,body{height:100%;margin:0}
body{background:#14171c;color:#e8ecf1;font:13px/1.5 -apple-system,BlinkMacSystemFont,'Segoe UI','Microsoft YaHei',sans-serif;overflow:hidden}
.iv{display:flex;flex-direction:column;height:100%}
.iv-bar{display:flex;align-items:center;gap:6px;padding:7px 10px;background:#1e232b;border-bottom:1px solid #2c333d;flex:0 0 auto;min-width:0}
.iv-name{font-weight:600;max-width:36%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.iv-dims{color:#8b95a3;font-size:11px;white-space:nowrap}
.iv-grow{flex:1 1 auto}
.iv-bar button,.iv-link{appearance:none;border:1px solid #38414e;background:#272e38;color:#e8ecf1;border-radius:6px;font:inherit;font-size:12px;line-height:1;padding:6px 10px;cursor:pointer;text-decoration:none;white-space:nowrap}
.iv-bar button:hover,.iv-link:hover{background:#333c49;border-color:#4a90d9}
.iv-bar button:active{background:#1b2028}
.iv-zoom{min-width:62px;text-align:center;font-variant-numeric:tabular-nums;color:#9ecbff}
.iv-viewport{position:relative;flex:1 1 auto;min-height:0;overflow:hidden;outline:none;cursor:grab;touch-action:none;background-color:#14171c;background-image:linear-gradient(45deg,#1b2028 25%,transparent 25%),linear-gradient(-45deg,#1b2028 25%,transparent 25%),linear-gradient(45deg,transparent 75%,#1b2028 75%),linear-gradient(-45deg,transparent 75%,#1b2028 75%);background-size:20px 20px;background-position:0 0,0 10px,10px -10px,-10px 0}
.iv-viewport.dragging{cursor:grabbing}
.iv-canvas{position:absolute;left:50%;top:50%;transform-origin:center center;will-change:transform}
.iv-canvas img{display:block;max-width:none;height:auto;user-select:none;-webkit-user-drag:none;transform-origin:center center}
.iv-hint{position:absolute;left:50%;bottom:10px;transform:translateX(-50%);padding:4px 12px;border-radius:99px;background:rgba(20,23,28,.78);color:#aab3c0;font-size:11px;pointer-events:none;white-space:nowrap}
.iv-error{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;color:#ff9b9b;font-size:14px;background:#14171c}
.hidden{display:none!important}
@media (max-width:720px){
.iv-bar{flex-wrap:wrap;gap:6px;padding:8px}
.iv-name{flex:1 1 100%;max-width:100%;font-size:12px}
.iv-dims,.iv-grow{display:none}
.iv-bar button,.iv-link{flex:1 1 auto;min-height:38px;padding:8px 9px;font-size:13px;text-align:center}
.iv-zoom{min-width:64px;flex:0 0 auto}
.iv-hint{width:92%;white-space:normal;text-align:center;font-size:10px;bottom:calc(8px + env(safe-area-inset-bottom))}
}`

func inlineMarkdown(value string) string {
	text := escapeHTML(value)
	text = reInlineCode.ReplaceAllString(text, "<code>$1</code>")
	text = reInlineImage.ReplaceAllString(text, `<img alt="$1" src="$2">`)
	text = reInlineLink.ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = reInlineBold.ReplaceAllString(text, "<strong>$1</strong>")
	return text
}

func wrapDoc(body string, title string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>` + escapeHTML(title) +
		`</title><style>` + docStyle + `</style></head><body>` + body + `</body></html>`
}

func renderMarkdown(source string, title string) string {
	if title == "" {
		title = "Markdown"
	}
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	inCode := false
	code := make([]string, 0, 32)
	inList := false
	paragraph := make([]string, 0, 16)

	flushParagraph := func() {
		if len(paragraph) > 0 {
			out = append(out, "<p>"+inlineMarkdown(strings.Join(paragraph, " "))+"</p>")
			paragraph = paragraph[:0]
		}
	}
	closeList := func() {
		if inList {
			out = append(out, "</ul>")
			inList = false
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flushParagraph()
			closeList()
			if inCode {
				out = append(out, "<pre><code>"+escapeHTML(strings.Join(code, "\n"))+"</code></pre>")
				code = code[:0]
				inCode = false
			} else {
				inCode = true
				code = code[:0]
			}
			continue
		}
		if inCode {
			code = append(code, line)
			continue
		}
		if trimmed == "" {
			flushParagraph()
			closeList()
			continue
		}
		if match := reHeading.FindStringSubmatch(line); match != nil {
			flushParagraph()
			closeList()
			level := len(match[1])
			out = append(out, fmt.Sprintf("<h%d>%s</h%d>", level, inlineMarkdown(match[2]), level))
			continue
		}
		if reHR.MatchString(trimmed) {
			flushParagraph()
			closeList()
			out = append(out, "<hr>")
			continue
		}
		if match := reListItem.FindStringSubmatch(line); match != nil {
			flushParagraph()
			if !inList {
				out = append(out, "<ul>")
				inList = true
			}
			out = append(out, "<li>"+inlineMarkdown(match[1])+"</li>")
			continue
		}
		closeList()
		if strings.HasPrefix(line, "> ") {
			flushParagraph()
			out = append(out, "<blockquote>"+inlineMarkdown(line[2:])+"</blockquote>")
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	if inCode {
		out = append(out, "<pre><code>"+escapeHTML(strings.Join(code, "\n"))+"</code></pre>")
	}
	flushParagraph()
	closeList()
	return wrapDoc(strings.Join(out, "\n"), title)
}

func renderTextPreview(text string, title string) string {
	if title == "" {
		title = "Text"
	}
	return wrapDoc("<h1>"+escapeHTML(title)+"</h1><pre><code>"+escapeHTML(text)+"</code></pre>", title)
}

func renderNotice(title string, message string) string {
	return wrapDoc("<h1>"+escapeHTML(title)+"</h1><p>"+escapeHTML(message)+"</p>", title)
}

type dirItem struct {
	Name  string
	Href  string
	Thumb string
	Dir   bool
	Size  int64
}

func formatSize(bytes int64) string {
	value := float64(bytes)
	switch {
	case value < 1024:
		return fmt.Sprintf("%.0f B", value)
	case value < 1024*1024:
		return fmt.Sprintf("%.1f KB", value/1024)
	case value < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", value/1024/1024)
	default:
		return fmt.Sprintf("%.1f GB", value/1024/1024/1024)
	}
}

func renderDirectory(title string, items []dirItem, hint string, view string) string {
	thumbs := view == "thumbs"
	folders := make([]dirItem, 0, len(items))
	images := make([]dirItem, 0, len(items))
	for _, item := range items {
		if item.Dir {
			folders = append(folders, item)
			continue
		}
		if item.Thumb != "" {
			images = append(images, item)
		}
	}
	switcher := `<div class="switch"><a href="?view=list"`
	if !thumbs {
		switcher += ` class="on"`
	}
	switcher += `>列表</a><a href="?view=thumbs"`
	if thumbs {
		switcher += ` class="on"`
	}
	switcher += ">缩略图"
	if len(images) > 0 {
		switcher += fmt.Sprintf(" (%d)", len(images))
	}
	switcher += `</a></div>`

	var body string
	if thumbs {
		folderLinks := make([]string, 0, len(folders))
		for _, item := range folders {
			href := item.Href
			if !strings.Contains(href, "?") {
				href += "?view=thumbs"
			}
			folderLinks = append(folderLinks, `<a href="`+escapeHTML(href)+`">`+escapeHTML(item.Name)+`/</a>`)
		}
		cards := make([]string, 0, len(images))
		for _, item := range images {
			cards = append(cards, `<a class="card" href="`+escapeHTML(item.Href)+`" title="`+escapeHTML(item.Name)+
				`"><img src="`+escapeHTML(item.Thumb)+`" loading="lazy" alt=""><span class="n">`+escapeHTML(item.Name)+
				`</span><span class="s">`+formatSize(item.Size)+`</span></a>`)
		}
		body = fmt.Sprintf(`<p class="hint">图片 %d 张 · 仅当前目录 · 点击缩略图查看原图</p>`, len(images))
		if len(folderLinks) > 0 {
			body += `<div class="folders">` + strings.Join(folderLinks, "") + `</div>`
		}
		if len(cards) > 0 {
			body += `<div class="grid">` + strings.Join(cards, "") + `</div>`
		} else {
			body += `<p class="empty">当前目录没有图片</p>`
		}
	} else {
		rows := make([]string, 0, len(items))
		for _, item := range items {
			size := formatSize(item.Size)
			if item.Dir {
				size = "文件夹"
			}
			rows = append(rows, `<li><a href="`+escapeHTML(item.Href)+`">`+escapeHTML(item.Name))
			if item.Dir {
				rows[len(rows)-1] += "/"
			}
			rows[len(rows)-1] += `</a><span>` + size + `</span></li>`
		}
		body = `<p class="hint">` + escapeHTML(hint) + `</p><ul>` + strings.Join(rows, "\n") + `</ul>`
	}
	head := `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>` + escapeHTML(title) +
		`</title><style>` + dirStyle + `</style></head><body>`
	return head + "<h1>" + escapeHTML(title) + "</h1>" + switcher + body + "</body></html>"
}

func renderImageViewer(name string, src string) string {
	safeName := escapeHTML(name)
	safeSrc := escapeHTML(src)
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + safeName + ` · 图片查看器</title>
<style>` + viewerStyle + `</style></head><body>
<div class="iv" data-image-viewer>
  <div class="iv-bar">
    <span class="iv-name" title="` + safeName + `">` + safeName + `</span>
    <span class="iv-dims" id="ivDims">载入中…</span>
    <span class="iv-grow"></span>
    <button type="button" id="ivOut" title="缩小（滚轮向下 / -）">−</button>
    <button type="button" id="ivZoom" class="iv-zoom" title="点击适应窗口">100%</button>
    <button type="button" id="ivIn" title="放大（滚轮向上 / +）">+</button>
    <button type="button" id="ivFit" title="适应窗口（0 / F）">适应</button>
    <button type="button" id="ivActual" title="原始尺寸（1）">1:1</button>
    <button type="button" id="ivRotate" title="旋转 90°（R）">↻</button>
    <a class="iv-link" href="` + safeSrc + `" target="_blank" rel="noopener" title="在新窗口打开原图">原图 ↗</a>
  </div>
  <div class="iv-viewport" id="ivViewport" tabindex="0" aria-label="图片区域，可滚轮缩放与拖拽平移">
    <div class="iv-canvas" id="ivCanvas"><img id="ivImage" src="` + safeSrc + `" alt="` + safeName + `" draggable="false"></div>
    <div class="iv-hint">滚轮缩放 · 按住拖拽平移 · 双击切换 1:1 / 适应</div>
    <div class="iv-error hidden" id="ivError">图片无法加载，或该格式不受浏览器支持</div>
  </div>
</div>
<script type="module" src="` + baseURL() + `/image-viewer.js"></script>
</body></html>`
}

func sortDirItems(items []dirItem) {
	sort.SliceStable(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
}
