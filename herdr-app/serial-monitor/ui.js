// Pure UI builders: theme in, elements out. No state lives here.
import { div } from "gpui-kit";
import { v_flex, h_flex, Input, Scrollbar, v_virtual_list } from "gpui-base";

export function header(t, view) {
  const { meta } = view;
  return h_flex().h(42).flex_none().px(12).gap(8).items_center().border_b(1).border_color(t.border)
    .child(statusDot(t, meta))
    .child(div().font_semibold().child("Serial Monitor"))
    .child(div().flex_1())
    .child(pill("sm-ts", "Time", view.timestamps, t, view.onToggleTimestamps))
    .child(pill("sm-follow", "Follow", view.follow, t, view.onToggleFollow))
    .child(pill("sm-clear", "Clear", false, t, view.onClear));
}

function statusDot(t, meta) {
  const color = meta ? (meta.open ? t.done : t.ansi1) : t.dim;
  return div().w(8).h(8).flex_none().rounded_full().bg(color);
}

function pill(id, label, active, t, onClick) {
  return div().id(id).h(22).px(8).flex_none().flex().items_center().rounded(6)
    .text_size(11).cursor_pointer()
    .bg(active ? t.accent : "#00000000")
    .text_color(active ? t.accent_foreground : t.muted)
    .border_1().border_color(active ? t.accent : t.border)
    .hover((s) => s.bg(t.hover))
    .on_click((_e, cx) => onClick(cx))
    .child(label);
}

export function toolbar(t, view) {
  const { meta, endingLabel } = view;
  return h_flex().h(34).flex_none().px(10).gap(6).items_center().border_b(1).border_color(t.border)
    .child(selector("sm-port", meta ? meta.device : (view.ports.length ? "Select port" : "No ports"),
      t, view.onPortMenu))
    .child(selector("sm-baud", meta ? `${meta.baud} bd` : "—", t, view.onBaudMenu))
    .child(selector("sm-ending", endingLabel, t, view.onEndingMenu));
}

function selector(id, label, t, onClick) {
  return div().id(id).h(26).px(8).flex_1().min_w(0).flex().items_center().justify_between().gap(6)
    .rounded(7).border_1().border_color(t.border).text_size(11).cursor_pointer()
    .hover((s) => s.bg(t.hover))
    .on_click((_e, cx) => onClick(cx))
    .child(div().min_w(0).truncate().text_color(t.foreground).child(label))
    .child(div().flex_none().text_color(t.dim).child("▾"));
}

export function menuBox(t, id, children, footer) {
  return v_flex().id(`sm-menu-${id}`).absolute().top(2).left(2).right(2)
    .bg(t.popover).border_1().border_color(t.border).rounded(8).shadow_md()
    .px(6).py(6).gap(2)
    .children(children)
    .when(footer, (e) => e.child(div().px(8).py(4).text_size(10).text_color(t.dim)
      .truncate().child(footer)));
}

export function optionRow(id, label, selected, t, onClick) {
  return h_flex().id(id).h(26).flex_none().px(8).gap(8).items_center().rounded(7).cursor_pointer()
    .when(selected, (e) => e.bg(t.selected))
    .hover((s) => s.bg(t.hover))
    .on_click((_e, cx) => onClick(cx))
    .child(div().w(12).text_size(10).text_color(t.accent).child(selected ? "✓" : ""))
    .child(div().flex_1().min_w(0).truncate().text_size(12).child(label));
}

export function emptyPortsRow(t) {
  return div().px(8).py(6).text_size(11).text_color(t.dim)
    .child("No serial ports — connect a board and wait");
}

// Follow mode: no scroller — the last lines, bottom-packed, so the newest
// line is always the bottom-most drawn element.
export function liveOutput(t, rows, mono, timestamps) {
  return v_flex().id("sm-live").size_full().justify_end().overflow_hidden()
    .children(rows.map((line) => outputRow(line, t, mono, timestamps)));
}

export function browseOutput(t, lines, scrollHandle, mono, timestamps) {
  return v_flex().size_full()
    .child(v_virtual_list("sm-out", lines.length, 18,
      (i) => String(lines[i]?.seq ?? i),
      (range) => lines.slice(range.start, range.end)
        .map((line) => outputRow(line, t, mono, timestamps)))
      .size_full().track_scroll(scrollHandle))
    .child(Scrollbar.vertical("sm-out").viewport_from_layout().absolute().inset_0());
}

function outputRow(line, t, mono, timestamps) {
  const style = { W: { color: t.ansi6, prefix: "» " }, F: { color: t.dim, prefix: "[flash] " } }[line.kind]
    || { color: t.foreground, prefix: "" };
  return h_flex().h(18).flex_none().px(8).items_center().font_family(mono)
    .when(timestamps, (e) => e.child(div().w(92).flex_none().text_size(10).text_color(t.dim)
      .child(timeLabel(line.ts))))
    .child(div().flex_1().min_w(0).truncate().text_size(11).text_color(style.color)
      .child(style.prefix + line.text));
}

function timeLabel(ms) {
  if (!ms) {
    return "";
  }
  const date = new Date(ms);
  const millis = String(date.getMilliseconds()).padStart(3, "0");
  return `${date.toLocaleTimeString([], { hour12: false })}.${millis}`;
}

export function emptyState(t, view) {
  const message = view.error ? " "
    : view.portId ? "Monitoring — new output appears here"
    : "No serial port connected";
  return div().absolute().top(10).w_full().flex().justify_center()
    .child(div().text_size(11).text_color(t.dim).child(message));
}

export function inputRow(t, view) {
  return h_flex().flex_none().p(10).gap(8).border_t(1).border_color(t.border)
    .child(inputField(t, view))
    .child(sendButton(t, view));
}

function inputField(t, view) {
  return div().id("sm-input").flex_1().min_w(0).h(32).px(10).flex().items_center().rounded(8)
    .border_1().border_color(view.typing ? t.accent : t.border).cursor_text()
    .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
    .child(Input.new(view.input).h_full().flex_1().text_size(12).text_color(t.foreground));
}

function sendButton(t, view) {
  return div().id("sm-send").h(32).px(14).flex().items_center().rounded(8).cursor_pointer()
    .bg(t.solid).text_color(t.solid_foreground).text_size(12)
    .hover((s) => s.bg(t.accent))
    .on_click((_e, cx) => view.onSend(cx))
    .child("Send");
}

export function noticeRow(t, color, text) {
  return div().flex_none().px(12).py(4).text_size(11).text_color(color).child(text);
}
