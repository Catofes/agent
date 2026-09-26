(() => {
  const teacher = document.body.dataset.mode === "teacher";
  const $ = (id) => document.getElementById(id);
  const roles = { wolf: "狼人", villager: "平民", seer: "预言家", witch: "女巫" };
  const phases = { night: "夜间", day: "白天", finished: "结局" };
  const channels = { public: "全体可见", wolves: "狼人私聊", private: "个人私密", teacher: "教师记录" };
  const groups = [
    ["通用策略", [["general", "所有身份的共同策略"]]],
    ["平民", [["villager_speak", "白天发言"], ["villager_vote", "白天投票"]]],
    ["狼人", [["wolf_discuss", "夜间和狼队友商议"], ["wolf_kill", "夜间选择目标"], ["wolf_speak", "白天发言"], ["wolf_vote", "白天投票"]]],
    ["预言家", [["seer_check", "夜间查验"], ["seer_speak", "白天发言"], ["seer_vote", "白天投票"]]],
    ["女巫", [["witch_action", "夜间使用解药或毒药"], ["witch_speak", "白天发言"], ["witch_vote", "白天投票"]]],
    ["出局", [["last_words", "遗言"]]],
  ];
  let me = null, currentGameID = "", cursor = 0, polling = false, rosterSignature = "", selected = new Set();
  let currentTournamentID = "", tournamentRunning = false, tournamentActionBusy = false, rosterCount = 0;

  async function api(path, options = {}) {
    const response = await fetch(path, { ...options, headers: { "Content-Type": "application/json", ...(options.headers || {}) } });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error?.message || `请求失败（${response.status}）`);
    return body;
  }

  function node(tag, value, className = "") {
    const element = document.createElement(tag);
    element.className = className;
    element.textContent = value;
    return element;
  }

  function renderPromptFields(prompts) {
    const root = $("promptFields");
    root.replaceChildren();
    groups.forEach(([name, fields], index) => {
      const details = document.createElement("details");
      details.className = "prompt-group";
      details.open = index < 2;
      details.append(node("summary", name));
      const grid = node("div", "", "prompt-grid");
      fields.forEach(([key, labelText]) => {
        const label = node("label", "", "prompt-field"), title = node("span", labelText);
        const input = document.createElement("textarea");
        input.name = key;
        input.id = `prompt-${key}`;
        input.maxLength = 800;
        input.value = prompts[key] || "";
        input.setAttribute("aria-label", `${name}：${labelText}`);
        label.append(title, input, node("small", "最多 800 字；留空将使用默认策略"));
        if (fields.length === 1) label.classList.add("full");
        grid.append(label);
      });
      details.append(grid);
      root.append(details);
    });
  }

  async function loadPrompts() {
    const data = await api("/api/werewolf/prompts");
    renderPromptFields(data.prompts || {});
    $("saveBadge").textContent = data.saved ? "已保存" : "使用默认 Prompt";
    $("promptsCard").classList.remove("hidden");
  }

  function renderRoster(students) {
    rosterSignature = JSON.stringify(students);
    rosterCount = students.length;
    const root = $("roster");
    root.replaceChildren();
    students.forEach((student) => {
      const label = node("label", "");
      const input = document.createElement("input");
      input.type = "checkbox";
      input.value = student.id;
      input.checked = selected.has(student.id);
      input.onchange = () => {
        if (input.checked && selected.size >= 6) {
          input.checked = false;
          $("actionStatus").textContent = "每局只能选择 6 名学生";
          return;
        }
        if (input.checked) selected.add(student.id);
        else selected.delete(student.id);
        updateSelection();
      };
      const copy = node("span", "");
      copy.append(node("strong", `${student.id} · ${student.name}`),
        node("small", `${student.has_persona ? "已设计 Soul" : "尚未设计 Soul"} · ${student.prompts_saved ? "已写 Prompt" : "默认 Prompt"}`));
      label.append(input, copy);
      label.classList.toggle("selected", input.checked);
      root.append(label);
    });
    updateSelection();
  }

  function updateSelection() {
    $("selectedCount").textContent = `已选 ${selected.size}/6`;
    $("roster").querySelectorAll("label").forEach((label) => label.classList.toggle("selected", label.querySelector("input").checked));
    $("startGame").disabled = selected.size !== 6 || $("teacherDisabled").classList.contains("hidden") === false || $("startGame").dataset.running === "true" || tournamentRunning;
  }

  function renderTournament(data) {
    const tournament = data.tournament;
    const enabled = data.enabled;
    $("tournamentCard").classList.toggle("hidden", !teacher && !enabled);
    currentTournamentID = tournament?.id || "";
    tournamentRunning = tournament?.status === "running";
    const labels = { running: "进行中", complete: "已完成", stopped: "已暂停", failed: "需检查", interrupted: "已中断" };
    $("tournamentBadge").textContent = tournament ? labels[tournament.status] || tournament.status : "尚未开赛";
    $("tournamentProgress").textContent = tournament
      ? `已完成 ${tournament.completed_matches} / ${tournament.total_matches} 场六人局；每人目标 ${tournament.target_games} 局。${tournament.error || ""}`
      : teacher ? `当前名单 ${rosterCount} 人；每人 20 局。` : "等待老师开始比赛。";
    const standings = $("standings");
    standings.replaceChildren();
    if (!data.standings?.length) {
      const row = document.createElement("tr"), cell = node("td", "开赛后显示所有参赛学生的得分。", "score-empty");
      cell.colSpan = 5;
      row.append(cell);
      standings.append(row);
    } else {
      data.standings.forEach((item, index) => {
        const row = document.createElement("tr");
        row.classList.toggle("self", item.student_id === me?.student?.id);
        row.append(node("td", String(index + 1)), node("td", `${item.student_id} · ${item.name}`),
          node("td", `${item.played} / ${tournament.target_games}`),
          node("td", `${item.wins} / ${item.losses} / ${item.draws}`),
          node("td", String(item.points)));
        standings.append(row);
      });
    }
    if (teacher) {
      $("startTournament").disabled = !enabled || tournamentRunning || tournamentActionBusy || rosterCount < 6;
      $("allowSystem").disabled = tournamentRunning || tournamentActionBusy;
      $("pauseTournament").classList.toggle("hidden", !tournamentRunning);
      $("resumeTournament").classList.toggle("hidden", !tournament || !["stopped", "failed", "interrupted"].includes(tournament.status));
      $("resumeTournament").disabled = tournamentActionBusy || !enabled;
      $("stopGame").classList.toggle("hidden", tournamentRunning || $("startGame").dataset.running !== "true");
      updateSelection();
    }
  }

  function renderGame(game) {
    $("gameCard").classList.remove("hidden");
    if (!game) {
      $("gameMeta").textContent = "等待老师选出 6 名 Agent。";
      $("gameBadge").textContent = "待开局";
      $("players").replaceChildren();
      $("roleNotice").textContent = teacher ? "角色开局后随机分配。" : "你可以先修改 Prompt，或者等待老师开局。";
      $("timeline").replaceChildren(node("div", "开局后将在这里显示主持人和各 Agent 的消息传递。", "timeline-empty"));
      if (teacher) {
        $("startGame").dataset.running = "false";
        $("stopGame").classList.add("hidden");
        updateSelection();
      }
      return;
    }
    const status = { running: "进行中", complete: "已结束", stopped: "已停止", failed: "出错", interrupted: "已中断" }[game.status] || game.status;
    $("gameBadge").textContent = status;
    $("gameMeta").textContent = `第 ${game.round} 轮 · ${phases[game.phase] || game.phase}${game.winner ? ` · ${game.winner === "wolves" ? "狼人获胜" : game.winner === "villagers" ? "好人获胜" : "平局"}` : ""}${game.error ? ` · ${game.error}` : ""}`;
    const mine = (game.players || []).find((item) => item.student_id === me?.student?.id);
    $("roleNotice").textContent = teacher
      ? "教师可查看所有角色与私密行动；学生只能看到自己的身份和自己应知的消息。"
      : mine ? `你的 Agent 是 ${mine.seat} 号${mine.role ? `，身份：${roles[mine.role]}` : ""}。${mine.alive ? "当前存活。" : "已出局，可继续观战。"}` : "你是本局观众，可以观看公开的发言、投票与结果。";
    const root = $("players");
    root.replaceChildren();
    (game.players || []).forEach((player) => {
      const card = node("div", "", `player${player.alive ? "" : " dead"}`);
      card.append(node("strong", `${player.seat} 号 · ${player.name}`), node("small", player.alive ? "存活" : "出局"));
      if (player.role) card.append(node("span", roles[player.role] || player.role, `role ${player.role}`));
      root.append(card);
    });
    if (teacher) {
      const running = game.status === "running";
      $("startGame").dataset.running = String(running);
      $("stopGame").classList.toggle("hidden", !running);
      updateSelection();
    }
  }

  function appendEvents(events) {
    const timeline = $("timeline");
    if (timeline.querySelector(".timeline-empty")) timeline.replaceChildren();
    events.forEach((event) => {
      const row = node("article", "", "event");
      row.append(node("div", String(event.id), "event-step"));
      const card = node("div", "", `event-card ${event.channel || "public"}`);
      card.append(node("div", `${phases[event.phase] || event.phase} · ${channels[event.channel] || ""}`, "meta"));
      if (event.actor_seat) {
        const flow = node("div", "", "flow");
        flow.append(node("span", "主持人"), node("b", "→"), node("span", `${event.actor_seat} 号 Agent`), node("b", "→"), node("span", event.channel === "public" ? "公开交流" : "私密交流"));
        card.append(flow);
      }
      card.append(node("div", event.text || "", "message"));
      if (event.prompt) {
        const details = document.createElement("details");
        details.append(node("summary", "查看发送的 Prompt 与原始回复"), node("pre", `主持人发送：\n${event.prompt}\n\nAgent 回复：\n${event.response || "（空）"}`));
        card.append(details);
      }
      row.append(card);
      timeline.append(row);
    });
  }

  async function refresh() {
    if (polling) return;
    polling = true;
    try {
      const prefix = teacher ? "/api/teacher/werewolf" : "/api/werewolf";
      const data = await api(`${prefix}/current`);
      if (teacher) {
        const roster = await api("/api/teacher/werewolf/roster");
        if (JSON.stringify(roster.students || []) !== rosterSignature) renderRoster(roster.students || []);
        $("teacherDisabled").classList.toggle("hidden", data.enabled);
        updateSelection();
      } else {
        $("disabled").classList.toggle("hidden", data.enabled);
        $("gameCard").classList.toggle("hidden", !data.enabled);
        $("promptsCard").classList.toggle("hidden", !data.enabled);
        if (data.enabled && !$("promptFields").children.length) await loadPrompts();
      }
      if (teacher || data.enabled) renderGame(data.game);
      const gameID = data.game?.id || "";
      if (gameID !== currentGameID) {
        currentGameID = gameID;
        cursor = 0;
        $("timeline").replaceChildren();
      }
      if (gameID) {
        const result = await api(`${prefix}/games/${encodeURIComponent(gameID)}/events?after=${cursor}`);
        appendEvents(result.events || []);
        cursor = result.next || cursor;
      }
      const tournament = await api(`${prefix}/tournament`);
      renderTournament(tournament);
      $("status").textContent = data.enabled ? "课堂狼人杀已开启，页面会自动更新。" : teacher ? "狼人杀尚未开启。" : "老师尚未开启狼人杀环节。";
    } catch (error) {
      $("status").textContent = `暂时无法读取：${error.message}。请确认已登录${teacher ? "教师管理台" : "学生工作台"}。`;
    } finally {
      polling = false;
    }
  }

  async function init() {
    try {
      me = await api(teacher ? "/api/teacher/me" : "/api/me");
      if (teacher) {
        $("teacherControls").classList.remove("hidden");
        const roster = await api("/api/teacher/werewolf/roster");
        renderRoster(roster.students || []);
      }
      await refresh();
      setInterval(refresh, 2500);
    } catch (error) {
      $("status").textContent = `请先返回${teacher ? "教师管理台" : "学生工作台"}登录：${error.message}`;
    }
  }

  if (!teacher) {
    $("promptForm").onsubmit = async (event) => {
      event.preventDefault();
      const payload = {};
      $("promptFields").querySelectorAll("textarea").forEach((input) => { payload[input.name] = input.value; });
      $("savePrompts").disabled = true;
      try {
        await api("/api/werewolf/prompts", { method: "PUT", body: JSON.stringify(payload) });
        $("saveBadge").textContent = "已保存";
        $("saveStatus").textContent = "已保存到当前课堂场次；下一局会使用最新 Prompt。";
      } catch (error) {
        $("saveStatus").textContent = `保存失败：${error.message}`;
      } finally {
        $("savePrompts").disabled = false;
      }
    };
  } else {
    $("startTournament").onclick = async () => {
      if (rosterCount < 6 || tournamentRunning || tournamentActionBusy) return;
      const matches = Math.ceil(rosterCount * 20 / 6);
      const fillers = matches * 6 - rosterCount * 20;
      const systemAllowed = $("allowSystem").checked;
      if (fillers && !systemAllowed) {
        $("tournamentActionStatus").textContent = `当前人数需要 ${fillers} 个系统补位席位，请勾选补位后开赛。`;
        return;
      }
      if (!confirm(`将为 ${rosterCount} 名学生安排每人 20 局，共 ${matches} 场六人局${fillers ? `，其中 ${fillers} 个席位由系统 Agent 补齐` : ""}。每局会多次调用 DeepSeek，可能持续较长时间并消耗 API 额度。确认开赛？`)) return;
      tournamentActionBusy = true;
      $("startTournament").disabled = true;
      $("tournamentActionStatus").textContent = "正在排赛并保存 Agent 设计…";
      try {
        await api("/api/teacher/werewolf/tournament", { method: "POST", body: JSON.stringify({ allow_system: systemAllowed }) });
        $("tournamentActionStatus").textContent = "比赛已开始，积分榜会自动更新。";
        await refresh();
      } catch (error) {
        $("tournamentActionStatus").textContent = `开赛失败：${error.message}`;
      } finally {
        tournamentActionBusy = false;
        $("startTournament").disabled = tournamentRunning || rosterCount < 6 || !$("teacherDisabled").classList.contains("hidden");
      }
    };
    $("pauseTournament").onclick = async () => {
      if (!currentTournamentID || !confirm("暂停比赛？已完成的场次和积分会保留，稍后可以继续。")) return;
      tournamentActionBusy = true;
      try {
        await api(`/api/teacher/werewolf/tournament/${encodeURIComponent(currentTournamentID)}/stop`, { method: "POST" });
        $("tournamentActionStatus").textContent = "比赛已暂停，已完成的场次和积分已保留。";
        await refresh();
      } catch (error) {
        $("tournamentActionStatus").textContent = `暂停失败：${error.message}`;
      } finally { tournamentActionBusy = false; }
    };
    $("resumeTournament").onclick = async () => {
      if (!currentTournamentID || tournamentActionBusy) return;
      tournamentActionBusy = true;
      try {
        await api(`/api/teacher/werewolf/tournament/${encodeURIComponent(currentTournamentID)}/resume`, { method: "POST" });
        $("tournamentActionStatus").textContent = "比赛已继续。";
        await refresh();
      } catch (error) {
        $("tournamentActionStatus").textContent = `继续失败：${error.message}`;
      } finally { tournamentActionBusy = false; }
    };
    $("startGame").onclick = async () => {
      if (selected.size !== 6) return;
      $("startGame").disabled = true;
      $("actionStatus").textContent = "正在分配角色并开始对局…";
      try {
        const result = await api("/api/teacher/werewolf/games", { method: "POST", body: JSON.stringify({ student_ids: [...selected] }) });
        $("actionStatus").textContent = `对局 ${result.game.id} 已开始。`;
        await refresh();
      } catch (error) {
        $("actionStatus").textContent = `开局失败：${error.message}`;
      } finally {
        updateSelection();
      }
    };
    $("stopGame").onclick = async () => {
      if (!currentGameID || !confirm("确认结束当前对局吗？本局不能继续。")) return;
      try {
        await api(`/api/teacher/werewolf/games/${encodeURIComponent(currentGameID)}/stop`, { method: "POST" });
        $("actionStatus").textContent = "对局已结束。";
        await refresh();
      } catch (error) {
        $("actionStatus").textContent = `结束失败：${error.message}`;
      }
    };
  }
  init();
})();
