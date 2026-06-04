// chat.js — live web chat client.
//
// The channel page server-renders recent history; this opens a Server-Sent
// Events stream and patches the DOM for every future event (new messages,
// edits, deletes, reactions, pins). Outbound actions (react/pin/reply/edit/
// delete) are fetch() POSTs that return 204 — the resulting Redis event comes
// back over the same stream and updates every open client, including this one,
// so there is no optimistic local mutation to keep in sync.
(function () {
  "use strict";
  var section = document.querySelector(".chat.channel[data-channel-id]");
  var log = document.getElementById("chat-log");
  if (!section || !log || typeof EventSource === "undefined") {
    return;
  }
  var channelID = section.getAttribute("data-channel-id");
  var selfHandle = section.getAttribute("data-self-handle") || "";
  var isSysop = section.getAttribute("data-sysop") === "1";
  var csrf = section.getAttribute("data-csrf") || "";
  // Keep in sync with chatReactionPalette in handlers_chat.go.
  var PALETTE = ["👍", "❤️", "😂", "🎉", "🔥", "👀"];
  var form = section.querySelector(".chat-send");
  var bodyInput = form.querySelector('input[name="body"]');
  var parentInput = form.querySelector('input[name="parent_id"]');
  var banner = form.querySelector(".reply-banner");
  var bannerWho = form.querySelector(".reply-banner-who");

  function post(path, params) {
    var body = new URLSearchParams(params).toString();
    return fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "X-CSRF-Token": csrf,
      },
      body: body,
    });
  }

  function atBottom() {
    return window.innerHeight + window.scrollY >= document.body.offsetHeight - 40;
  }

  function msgEl(id) {
    return log.querySelector('[data-msg-id="' + id + '"]');
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  // buildMessage mirrors the server's chat_channel.html.tmpl <li> so appended
  // messages carry the same hooks (toolbar, reactions, badges) as rendered ones.
  function buildMessage(f) {
    var li = document.createElement("li");
    li.className = "chat-msg";
    li.id = "msg-" + f.id;
    li.setAttribute("data-msg-id", f.id);
    if (f.is_own) {
      li.setAttribute("data-own", "1");
      if (f.raw != null) li.setAttribute("data-raw", f.raw);
    }

    if (f.parent_id) {
      var marker = el("a", "reply-marker muted");
      marker.href = "#msg-" + f.parent_id;
      var parentEl = msgEl(f.parent_id);
      var who = parentEl ? parentEl.querySelector(".chat-author") : null;
      marker.textContent = "↳ replying to " + (who ? who.textContent : "a message");
      li.appendChild(marker);
    }

    var head = el("div", "msg-head");
    if (f.is_action) {
      var actionWrap = el("span", "chat-body action");
      actionWrap.appendChild(document.createTextNode("* "));
      actionWrap.appendChild(el("span", "chat-author", "@" + f.handle));
      actionWrap.appendChild(document.createTextNode(" "));
      var ab = el("span");
      ab.innerHTML = f.body; // server-escaped
      actionWrap.appendChild(ab);
      head.appendChild(actionWrap);
      var apin = el("span", "pin-mark", "★");
      apin.hidden = true;
      head.appendChild(apin);
    } else {
      head.appendChild(el("span", "chat-author", "@" + f.handle));
      if (f.is_sysop) {
        head.appendChild(document.createTextNode(" "));
        head.appendChild(el("span", "chip sysop", "SYSOP"));
      }
      head.appendChild(document.createTextNode(" "));
      head.appendChild(el("span", "chat-time muted", f.time || ""));
      head.appendChild(document.createTextNode(" "));
      var pin = el("span", "pin-mark", "★");
      pin.hidden = true;
      head.appendChild(pin);
      var ed = el("span", "edited muted", "(edited)");
      ed.hidden = true;
      head.appendChild(ed);
      head.appendChild(document.createTextNode(" "));
      var bodySpan = el("span", "chat-body");
      bodySpan.innerHTML = f.body; // server-escaped HTML
      head.appendChild(bodySpan);
    }
    li.appendChild(head);

    li.appendChild(el("div", "chat-reactions"));

    var toolbar = el("div", "msg-toolbar");
    var palette = el("span", "react-palette");
    PALETTE.forEach(function (em) {
      var b = el("button", "react-add", em);
      b.type = "button";
      b.setAttribute("data-emoji", em);
      palette.appendChild(b);
    });
    toolbar.appendChild(palette);
    var reply = el("button", "act-reply", "reply");
    reply.type = "button";
    reply.setAttribute("data-handle", f.handle);
    toolbar.appendChild(reply);
    var pinBtn = el("button", "act-pin", "pin");
    pinBtn.type = "button";
    toolbar.appendChild(pinBtn);
    var editBtn = el("button", "act-edit", "edit");
    editBtn.type = "button";
    editBtn.hidden = true;
    toolbar.appendChild(editBtn);
    var delBtn = el("button", "act-delete", "delete");
    delBtn.type = "button";
    delBtn.hidden = true;
    toolbar.appendChild(delBtn);
    li.appendChild(toolbar);

    var badge = el("a", "reply-badge muted");
    badge.href = "#msg-" + f.id;
    badge.hidden = true;
    badge.appendChild(document.createTextNode("0 "));
    badge.appendChild(el("span", "rb-word", "replies"));
    li.appendChild(badge);

    return li;
  }

  function appendMessage(f) {
    if (msgEl(f.id)) return; // dedupe (load↔subscribe race or echo)
    var stick = atBottom();
    log.appendChild(buildMessage(f));
    if (f.parent_id) bumpReplyBadge(f.parent_id, 1);
    refreshControls();
    if (stick) window.scrollTo(0, document.body.scrollHeight);
  }

  function applyEdit(f) {
    var m = msgEl(f.id);
    if (!m) return;
    var body = m.querySelector(".chat-body");
    if (body) body.innerHTML = f.body;
    var ed = m.querySelector(".edited");
    if (ed) ed.hidden = false;
    if (f.is_own && f.raw != null) m.setAttribute("data-raw", f.raw);
  }

  function applyDelete(f) {
    var m = msgEl(f.id);
    if (!m) return;
    m.innerHTML = "";
    m.classList.add("deleted");
    var head = el("div", "msg-head");
    head.appendChild(el("span", "chat-body muted", "(deleted)"));
    m.appendChild(head);
    refreshControls();
  }

  function reactionChip(m, emoji) {
    var box = m.querySelector(".chat-reactions");
    return box ? box.querySelector('[data-emoji="' + cssEscape(emoji) + '"]') : null;
  }

  function cssEscape(s) {
    if (window.CSS && CSS.escape) return CSS.escape(s);
    return s.replace(/["\\]/g, "\\$&");
  }

  function updateReaction(f, delta) {
    var m = msgEl(f.id);
    if (!m) return;
    var box = m.querySelector(".chat-reactions");
    if (!box) return;
    var chip = reactionChip(m, f.emoji);
    if (delta > 0) {
      if (!chip) {
        chip = el("button", "chip react-chip");
        chip.type = "button";
        chip.setAttribute("data-emoji", f.emoji);
        chip.appendChild(document.createTextNode(f.emoji + " "));
        chip.appendChild(el("span", "rc-count", "0"));
        box.appendChild(chip);
      }
      var c = chip.querySelector(".rc-count");
      c.textContent = String((parseInt(c.textContent, 10) || 0) + 1);
      if (f.is_self) chip.classList.add("reacted");
    } else if (chip) {
      var cc = chip.querySelector(".rc-count");
      var n = (parseInt(cc.textContent, 10) || 1) - 1;
      if (n <= 0) {
        chip.remove();
      } else {
        cc.textContent = String(n);
        if (f.is_self) chip.classList.remove("reacted");
      }
    }
  }

  function togglePin(f) {
    var m = msgEl(f.id);
    if (!m) return;
    m.classList.toggle("pinned", !!f.is_pinned);
    var mark = m.querySelector(".pin-mark");
    if (mark) mark.hidden = !f.is_pinned;
    var btn = m.querySelector(".act-pin");
    if (btn) btn.textContent = f.is_pinned ? "unpin" : "pin";
  }

  function bumpReplyBadge(parentID, delta) {
    var m = msgEl(parentID);
    if (!m) return;
    var badge = m.querySelector(".reply-badge");
    if (!badge) return;
    var word = badge.querySelector(".rb-word");
    var n = (parseInt(badge.firstChild.textContent, 10) || 0) + delta;
    badge.firstChild.textContent = n + " ";
    if (word) word.textContent = n === 1 ? "reply" : "replies";
    badge.hidden = n <= 0;
  }

  // refreshControls sets per-message edit/delete visibility: delete on own
  // messages (or any, for a sysop); edit only on the viewer's most recent
  // editable message (the service can only edit "last own in channel").
  function refreshControls() {
    var lastOwn = null;
    log.querySelectorAll(".chat-msg").forEach(function (m) {
      var own = m.getAttribute("data-own") === "1";
      var del = m.querySelector(".act-delete");
      var edt = m.querySelector(".act-edit");
      if (del) del.hidden = !(own || isSysop);
      if (edt) {
        edt.hidden = true;
        if (own) lastOwn = m;
      }
    });
    if (lastOwn) {
      var e = lastOwn.querySelector(".act-edit");
      if (e) e.hidden = false;
    }
  }

  function setReplyTarget(msgID, handle) {
    parentInput.value = msgID;
    if (bannerWho) bannerWho.textContent = "@" + handle;
    if (banner) banner.hidden = false;
    bodyInput.focus();
  }

  function clearReplyTarget() {
    parentInput.value = "";
    if (banner) banner.hidden = true;
  }

  // Inline edit: swap the body for an input prefilled from data-raw.
  function startEdit(m) {
    if (m.querySelector(".edit-input")) return;
    var raw = m.getAttribute("data-raw") || "";
    var body = m.querySelector(".chat-body");
    if (!body) return;
    var input = el("input", "edit-input");
    input.type = "text";
    input.value = raw;
    input.maxLength = 4000;
    body.replaceWith(input);
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
    function finish(save) {
      var val = input.value.trim();
      var restored = el("span", "chat-body");
      restored.innerHTML = body.innerHTML; // restore prior render; SSE will refresh on save
      input.replaceWith(restored);
      if (save && val) post("/chat/" + channelID + "/edit", { body: val });
    }
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); finish(true); }
      else if (e.key === "Escape") { e.preventDefault(); finish(false); }
    });
    input.addEventListener("blur", function () { finish(false); });
  }

  // Delegated click handling for all per-message actions.
  log.addEventListener("click", function (e) {
    var btn = e.target.closest("button, .reply-marker, .reply-badge");
    if (!btn) return;
    var m = btn.closest(".chat-msg");
    if (!m) return;
    var msgID = m.getAttribute("data-msg-id");

    if (btn.classList.contains("react-add") || btn.classList.contains("react-chip")) {
      var emoji = btn.getAttribute("data-emoji");
      var existing = reactionChip(m, emoji);
      var reacted = existing && existing.classList.contains("reacted");
      post("/chat/" + channelID + "/" + (reacted ? "unreact" : "react"), { message_id: msgID, emoji: emoji });
    } else if (btn.classList.contains("act-reply")) {
      setReplyTarget(msgID, btn.getAttribute("data-handle"));
    } else if (btn.classList.contains("act-pin")) {
      var pin = !m.classList.contains("pinned");
      post("/chat/" + channelID + "/pin", { message_id: msgID, pin: pin ? "1" : "0" });
    } else if (btn.classList.contains("act-edit")) {
      startEdit(m);
    } else if (btn.classList.contains("act-delete")) {
      if (window.confirm("Delete this message?")) {
        post("/chat/" + channelID + "/delete", { message_id: msgID });
      }
    }
  });

  if (banner) {
    var cancel = banner.querySelector(".reply-cancel");
    if (cancel) cancel.addEventListener("click", clearReplyTarget);
  }
  // Clear the reply target once the message is sent (the form navigates away,
  // but clear defensively in case of validation bounce-back).
  form.addEventListener("submit", function () { setTimeout(clearReplyTarget, 0); });

  var es = new EventSource("/chat/" + channelID + "/stream");
  es.onmessage = function (e) {
    var f;
    try { f = JSON.parse(e.data); } catch (err) { return; }
    switch (f.kind) {
      case "message_created": appendMessage(f); break;
      case "message_edited": applyEdit(f); break;
      case "message_deleted": applyDelete(f); break;
      case "reaction_added": updateReaction(f, 1); break;
      case "reaction_removed": updateReaction(f, -1); break;
      case "pin_changed": togglePin(f); break;
    }
  };

  refreshControls();
  window.scrollTo(0, document.body.scrollHeight);
})();
