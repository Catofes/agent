(() => {
  const teacher = document.body.dataset.mode === "teacher";
  const $ = (id) => document.getElementById(id);
  const roles = { wolf: "狼人", villager: "平民", seer: "预言家", witch: "女巫" };
  const phases = { night: "夜间", day: "白天", finished: "结局" };
  const channels = { public: "全体可见", wolves: "狼人私聊", private: "个人私密", teacher: "教师记录" };
  const groups = [
    ["平民", [["villager_speak", "白天发言 Skill"], ["villager_vote", "白天投票 Skill"]]],
    ["狼人", [["wolf_discuss", "夜间商议 Skill"], ["wolf_kill", "夜间选择目标 Skill"], ["wolf_speak", "白天发言 Skill"], ["wolf_vote", "白天投票 Skill"]]],
    ["预言家", [["seer_check", "夜间查验 Skill"], ["seer_speak", "白天发言 Skill"], ["seer_vote", "白天投票 Skill"]]],
    ["女巫", [["witch_action", "夜间用药 Skill"], ["witch_speak", "白天发言 Skill"], ["witch_vote", "白天投票 Skill"]]],
    ["通用行动", [["last_words", "遗言 Skill"]]],
  ];
  let me = null, currentGameID = "", cursor = 0, polling = false, rosterSignature = "", selected = new Set();
  let currentTournamentID = "", tournamentRunning = false, tournamentActionBusy = false, rosterCount = 0;
  let manager = false, classroomEnabled = false, demoGameID = "", demoCursor = 0, demoBusy = false;
  let musicContext = null, musicTimer = null, musicStep = 0;

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
    const field = (key, title, group) => {
      const label = node("label", "", "prompt-field");
      const input = document.createElement("textarea");
      input.name = key;
      input.id = `prompt-${key}`;
      input.maxLength = 800;
      input.value = prompts[key] || "";
      input.setAttribute("aria-label", `${group}：${title}`);
      label.append(node("span", title), input, node("small", "最多 800 字；留空使用课堂默认内容"));
      return label;
    };
    const soul = node("section", "", "soul-panel");
    soul.append(node("span", "01 · 所有身份都会使用", "design-kicker"), node("h3", "狼人杀 Soul"),
      node("p", "写你的 Agent 在整局游戏中坚持的判断方式、表达风格和原则。"), field("general", "狼人杀 Soul", "狼人杀 Soul"));
    root.append(soul, node("div", "02 · 按身份调用的行动 Skill", "design-kicker role-heading"));
    const tabs = node("div", "", "role-tabs"), panels = node("div", "", "role-panels");
    tabs.setAttribute("role", "tablist");
    groups.forEach(([name, fields], index) => {
      const tab = node("button", name, "role-tab");
      tab.type = "button";
      tab.id = `role-tab-${index}`;
      tab.setAttribute("role", "tab");
      tab.setAttribute("aria-controls", `role-panel-${index}`);
      tab.setAttribute("aria-selected", String(index === 0));
      const panel = node("section", "", "role-panel");
      panel.id = `role-panel-${index}`;
      panel.setAttribute("role", "tabpanel");
      panel.setAttribute("aria-labelledby", tab.id);
      panel.hidden = index !== 0;
      panel.append(node("h3", `${name} · Skill`), node("p", "只有 Agent 获得这一身份并执行对应行动时，才会使用本组 Skill。"));
      const grid = node("div", "", "prompt-grid");
      fields.forEach(([key, label]) => grid.append(field(key, label, name)));
      panel.append(grid);
      tab.onclick = () => {
        tabs.querySelectorAll("[role=tab]").forEach((item) => item.setAttribute("aria-selected", String(item === tab)));
        panels.querySelectorAll("[role=tabpanel]").forEach((item) => { item.hidden = item !== panel; });
        tab.focus();
      };
      tab.onkeydown = (event) => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const all = [...tabs.querySelectorAll("[role=tab]")];
        const position = all.indexOf(tab);
        const next = event.key === "Home" ? 0 : event.key === "End" ? all.length - 1
          : (position + (event.key === "ArrowRight" ? 1 : -1) + all.length) % all.length;
        all[next].click();
      };
      tabs.append(tab);
      panels.append(panel);
    });
    root.append(tabs, panels);
  }

  async function loadPrompts() {
    const data = await api("/api/werewolf/prompts");
    renderPromptFields(data.prompts || {});
    $("saveBadge").textContent = data.saved ? "已保存" : "使用默认 Soul / Skill";
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
          node("small", `${student.has_persona ? "已设计 Soul" : "尚未设计 Soul"} · ${student.prompts_saved ? "已写狼人杀 Skill" : "默认 Skill"}`));
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
      if (!running && $("actionStatus").textContent === `对局 ${game.id} 已开始。`) {
        $("actionStatus").textContent = game.status === "complete"
          ? `本局已结束：${game.winner === "wolves" ? "狼人获胜" : game.winner === "villagers" ? "好人获胜" : "平局"}。`
          : `本局已${game.status === "failed" ? "出错" : "停止"}。`;
      }
      updateSelection();
    }
  }

  function appendEvents(events, timelineID = "timeline", isDemo = false) {
    const timeline = $(timelineID);
    if (timeline.querySelector(".timeline-empty")) timeline.replaceChildren();
    events.forEach((event) => {
      const row = node("article", "", "event");
      row.append(node("div", String(event.id), "event-step"));
      const card = node("div", "", `event-card ${event.channel || "public"}`);
      card.append(node("div", `${phases[event.phase] || event.phase} · ${channels[event.channel] || ""}${event.actor_role ? ` · ${roles[event.actor_role]}` : ""}`, "meta"));
      if (event.actor_seat) {
        const flow = node("div", "", "flow");
        flow.append(node("span", "主持人"), node("b", "→"), node("span", `${event.actor_seat} 号${event.actor_system ? "系统" : "我的"} Agent`), node("b", "→"), node("span", event.channel === "public" ? "公开交流" : "私密交流"));
        card.append(flow);
      }
      card.append(node("div", event.text || "", "message"));
      if (event.prompt) {
        const ingredients = node("div", "", "prompt-ingredients");
        [["工作坊 Soul", event.workshop_soul], ["狼人杀 Soul", event.werewolf_soul], ["工作坊 Skill", event.base_skills],
          [event.action_skill_name || "行动 Skill", event.action_skill]].forEach(([label, value]) => {
          if (!value) return;
          const part = node("div", "", "ingredient");
          part.append(node("b", label), node("span", value));
          ingredients.append(part);
        });
        card.append(ingredients);
        const details = document.createElement("details");
        details.append(node("summary", "展开完整系统提示、当前请求与 Agent 原始回复"), node("pre", `主持人发送：\n${event.prompt}\n\nAgent 回复：\n${event.response || "（空）"}`));
        card.append(details);
      }
      row.append(card);
      timeline.append(row);
    });
    if (isDemo && events.length) timeline.lastElementChild?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }

  function renderControl(enabled) {
    classroomEnabled = enabled;
    if (!manager || teacher) return;
    $("managerCard").classList.remove("hidden");
    $("controlBadge").textContent = enabled ? "学生端已开放" : "学生端已关闭";
    $("toggleWerewolf").textContent = enabled ? "关闭学生端狼人杀" : "向学生开放狼人杀";
  }

  function renderDemo(game) {
    $("demoCard").classList.remove("hidden");
    $("demoStage").classList.toggle("hidden", !game);
    $("demoBadge").textContent = game ? ({ running: "进行中", complete: "已结束", stopped: "已停止", failed: "出错", interrupted: "已中断" }[game.status] || game.status) : "待演示";
    $("startDemo").disabled = demoBusy || game?.status === "running";
    $("stopDemo").classList.toggle("hidden", game?.status !== "running");
    if (!game) return;
    $("demoPhase").textContent = `第 ${game.round} 轮 · ${phases[game.phase] || game.phase}`;
    $("demoMeta").textContent = game.winner ? ({ wolves: "狼人阵营获胜", villagers: "好人阵营获胜", draw: "本局平局" }[game.winner] || game.winner) : game.error || "主持人正在按流程发出请求";
    const players = $("demoPlayers");
    players.replaceChildren();
    (game.players || []).forEach((player) => {
      const card = node("div", "", `demo-player ${player.mine ? "mine" : ""} ${player.alive ? "" : "dead"}`);
      card.append(node("small", `${player.seat} 号 · ${player.mine ? "我的 Agent" : player.name}`), node("strong", roles[player.role] || player.role),
        node("span", player.alive ? "存活" : "出局"));
      players.append(card);
    });
  }

  async function refreshDemo() {
    if (!manager || teacher) return;
    const data = await api("/api/werewolf/demo");
    const game = data.game;
    renderDemo(game);
    if ((game?.id || "") !== demoGameID) {
      demoGameID = game?.id || "";
      demoCursor = 0;
      $("demoTimeline").replaceChildren(node("div", "等待主持人发出第一条消息…", "timeline-empty"));
    }
    if (demoGameID) {
      const result = await api(`/api/werewolf/demo/events?after=${demoCursor}`);
      appendEvents(result.events || [], "demoTimeline", true);
      demoCursor = result.next || demoCursor;
      $("demoProgress").textContent = `${$("demoTimeline").querySelectorAll(".event").length} 步`;
      if (game?.status === "complete") $("demoStatus").textContent = "演示完成。可切换身份或修改 Soul / Skill 后再次演示。";
      if (game?.status === "failed") $("demoStatus").textContent = game.error || "演示未完成，请检查模型配置。";
    }
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
        renderControl(data.enabled);
        $("disabled").classList.toggle("hidden", data.enabled || manager);
        $("gameCard").classList.toggle("hidden", !data.enabled);
        $("promptsCard").classList.toggle("hidden", !data.enabled && !manager);
        if ((data.enabled || manager) && !$("promptFields").children.length) await loadPrompts();
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
      if (manager && !teacher) await refreshDemo();
      $("status").textContent = data.enabled ? "课堂狼人杀已开启，页面会自动更新。" : teacher ? "狼人杀尚未开启。" : "老师尚未开启狼人杀环节。";
      if (manager && !data.enabled) $("status").textContent = "学生端暂未开放；你仍可设置 Agent 并做现场演示。";
    } catch (error) {
      $("status").textContent = `暂时无法读取：${error.message}。请确认已登录${teacher ? "教师管理台" : "学生工作台"}。`;
    } finally {
      polling = false;
    }
  }

  async function init() {
    try {
      me = await api("/api/me");
      manager = me.werewolf_manager === true;
      if (teacher) {
        if (manager) {
          $("teacherBack").href = "/werewolf";
          $("teacherBack").textContent = "← 返回我的狼人杀工作台";
          $("enableLink").href = "/werewolf";
          $("enableLink").textContent = "我的狼人杀工作台";
        }
        $("teacherControls").classList.remove("hidden");
        const roster = await api("/api/teacher/werewolf/roster");
        renderRoster(roster.students || []);
      } else if (manager) {
        $("managerCard").classList.remove("hidden");
        $("demoCard").classList.remove("hidden");
      }
      await refresh();
      setInterval(refresh, 2500);
    } catch (error) {
      $("status").textContent = `请先返回${teacher ? "教师管理台或 A01/A02 工作台" : "学生工作台"}登录：${error.message}`;
    }
  }

  async function savePromptDesign() {
    const payload = {};
    $("promptFields").querySelectorAll("textarea").forEach((input) => { payload[input.name] = input.value; });
    const result = await api("/api/werewolf/prompts", { method: "PUT", body: JSON.stringify(payload) });
    $("saveBadge").textContent = "已保存";
    $("saveStatus").textContent = "Soul 与各身份 Skill 已保存；下一局将使用最新版本。";
    return result;
  }

  function playMusicNote() {
    if (!musicContext) return;
    const sequence = [130.81, 155.56, 196, 155.56, 116.54, 146.83, 174.61, 146.83];
    const dark = demoGameID && $("demoPhase").textContent.includes("夜间");
    const time = musicContext.currentTime;
    const note = sequence[musicStep++ % sequence.length];
    [note, note / 2].forEach((frequency, index) => {
      const oscillator = musicContext.createOscillator(), gain = musicContext.createGain();
      oscillator.type = index ? "sine" : (dark ? "triangle" : "sine");
      oscillator.frequency.setValueAtTime(frequency, time);
      gain.gain.setValueAtTime(0.0001, time);
      gain.gain.exponentialRampToValueAtTime(index ? 0.012 : 0.021, time + 0.08);
      gain.gain.exponentialRampToValueAtTime(0.0001, time + 0.58);
      oscillator.connect(gain).connect(musicContext.destination);
      oscillator.start(time);
      oscillator.stop(time + 0.6);
    });
  }

  async function toggleMusic() {
    if (musicTimer) {
      clearInterval(musicTimer);
      musicTimer = null;
      await musicContext?.close();
      musicContext = null;
      $("demoMusic").textContent = "♫ 开启氛围音乐";
      $("demoMusic").setAttribute("aria-pressed", "false");
      return;
    }
    const AudioContextClass = window.AudioContext || window.webkitAudioContext;
    if (!AudioContextClass) { $("demoStatus").textContent = "当前浏览器不支持网页音乐。"; return; }
    musicContext = new AudioContextClass();
    await musicContext.resume();
    musicStep = 0;
    playMusicNote();
    musicTimer = setInterval(playMusicNote, 650);
    $("demoMusic").textContent = "♫ 关闭氛围音乐";
    $("demoMusic").setAttribute("aria-pressed", "true");
  }

  if (!teacher) {
    $("promptForm").onsubmit = async (event) => {
      event.preventDefault();
      $("savePrompts").disabled = true;
      try {
        await savePromptDesign();
      } catch (error) {
        $("saveStatus").textContent = `保存失败：${error.message}`;
      } finally {
        $("savePrompts").disabled = false;
      }
    };
    $("toggleWerewolf").onclick = async () => {
      $("toggleWerewolf").disabled = true;
      try {
        const result = await api("/api/werewolf/control", { method: "PUT", body: JSON.stringify({ enabled: !classroomEnabled }) });
        renderControl(result.enabled);
        $("controlStatus").textContent = result.enabled ? "已向学生开放狼人杀。" : "已关闭学生端狼人杀。";
        await refresh();
      } catch (error) {
        $("controlStatus").textContent = `切换失败：${error.message}`;
      } finally { $("toggleWerewolf").disabled = false; }
    };
    $("startDemo").onclick = async () => {
      if (demoBusy) return;
      demoBusy = true;
      $("startDemo").disabled = true;
      $("demoStatus").textContent = "正在保存 Soul 与 Skill，并为六个 Agent 分配身份…";
      try {
        await savePromptDesign();
        await api("/api/werewolf/demo", { method: "POST", body: JSON.stringify({ role: $("demoRole").value }) });
        $("demoStatus").textContent = "演示已开始。展开卡片可查看本步使用的 Soul、Skill 与完整提示。";
        await refreshDemo();
      } catch (error) {
        $("demoStatus").textContent = `演示未开始：${error.message}`;
      } finally { demoBusy = false; }
    };
    $("stopDemo").onclick = async () => {
      try {
        await api("/api/werewolf/demo/stop", { method: "POST" });
        $("demoStatus").textContent = "演示已停止。";
        await refreshDemo();
      } catch (error) { $("demoStatus").textContent = `停止失败：${error.message}`; }
    };
    $("demoMusic").onclick = toggleMusic;
    window.addEventListener("pagehide", () => { if (musicTimer) clearInterval(musicTimer); musicContext?.close(); });
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
