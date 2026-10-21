(() => {
  const teacher = document.body.dataset.mode === "teacher";
  const $ = (id) => document.getElementById(id);
  const roles = { wolf: "狼人", villager: "平民", seer: "预言家", witch: "女巫" };
  const phases = { night: "夜间", day: "白天", finished: "结局" };
  const channels = { public: "全体可见", wolves: "狼人私聊", private: "个人私密", teacher: "教师记录" };
  const roleOptions = [["", "所有身份"], ["villager", "平民"], ["wolf", "狼人"], ["seer", "预言家"], ["witch", "女巫"]];
  const actionOptions = [["", "所有环节"], ["discuss", "夜间商议"], ["kill", "狼人选择目标"],
    ["check", "预言家查验"], ["witch", "女巫用药"], ["speak", "白天发言"], ["vote", "白天投票"], ["last_words", "遗言"]];
  let me = null, currentGameID = "", cursor = 0, polling = false, rosterSignature = "", selected = new Set();
  let currentTournamentID = "", tournamentRunning = false, tournamentActionBusy = false, rosterCount = 0, tournamentSelected = null;
  let manager = false, classroomEnabled = false, demoGameID = "", demoCursor = 0, demoBusy = false;
  let demoPlayback = null, livePlayback = null, replayPlayback = null, matchListKey = "";
  let demoGame = null, teacherGame = null, replayGame = null, replayMatchIndex = 0;
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
    const soul = node("section", "", "soul-panel");
    const soulField = node("label", "", "prompt-field");
    const soulInput = document.createElement("textarea");
    soulInput.id = "werewolfSoul";
    soulInput.maxLength = 800;
    soulInput.value = prompts.general || "";
    soulInput.placeholder = "写下你的 Agent 在整局游戏中的性格、判断原则和表达方式";
    soulField.append(node("span", "我的 Soul"), soulInput, node("small", "最多 800 字；留空也可以参加对局"));
    soul.append(node("span", "01 · 所有身份都会使用", "design-kicker"), node("h3", "狼人杀 Soul"),
      node("p", "由你自己写，不预设身份或策略。"), soulField);
    const skillsHead = node("div", "", "skill-editor-head");
    const skillsTitle = node("div", "");
    skillsTitle.append(node("span", "02 · 自己创建", "design-kicker"), node("h3", "我的 Skill"),
      node("p", "自行命名并填写使用条件与内容。身份和环节只是可选的使用范围，不会替你创建 Skill。"));
    const add = node("button", "＋ 新建 Skill", "secondary");
    add.type = "button";
    add.id = "addWerewolfSkill";
    skillsHead.append(skillsTitle, add);
    const list = node("div", "", "skill-editor-list");
    list.id = "werewolfSkillList";
    const empty = node("p", "还没有创建 Skill。可以只保存 Soul，之后再添加。", "skill-empty");
    empty.id = "werewolfSkillEmpty";
    root.append(soul, skillsHead, empty, list);
    add.onclick = () => {
      if (list.children.length >= 16) { $("saveStatus").textContent = "最多创建 16 个 Skill。"; return; }
      list.append(createSkillCard({ enabled: true }));
      empty.classList.add("hidden");
      list.lastElementChild.querySelector("[data-field=name]").focus();
    };
    (prompts.skills || []).forEach((skill) => list.append(createSkillCard(skill)));
    empty.classList.toggle("hidden", list.children.length > 0);
  }

  function createSkillCard(skill) {
    const card = node("section", "", "skill-card");
    const heading = node("div", "", "skill-card-head");
    const title = node("h4", skill.name || "新 Skill");
    const remove = node("button", "删除", "secondary");
    remove.type = "button";
    remove.setAttribute("aria-label", `删除 ${skill.name || "新 Skill"}`);
    remove.onclick = () => {
      card.remove();
      $("werewolfSkillEmpty").classList.toggle("hidden", $("werewolfSkillList").children.length > 0);
    };
    heading.append(title, remove);
    const field = (labelText, fieldName, tag, value, max) => {
      const label = node("label", "", "prompt-field");
      const input = document.createElement(tag);
      input.dataset.field = fieldName;
      input.maxLength = max;
      input.required = true;
      input.value = value || "";
      label.append(node("span", labelText), input);
      return label;
    };
    const name = field("Skill 名称", "name", "input", skill.name, 60);
    name.querySelector("input").placeholder = "自己给这个能力起名";
    name.querySelector("input").oninput = (event) => { title.textContent = event.target.value || "新 Skill"; };
    const summary = field("能力说明（可选）", "summary", "input", skill.summary, 200);
    summary.querySelector("input").required = false;
    summary.querySelector("input").placeholder = "简要说明这个 Skill 能帮 Agent 做什么";
    const when = field("何时使用", "when_to_use", "input", skill.when_to_use, 200);
    when.querySelector("input").placeholder = "写下 Agent 应在什么情况下使用";
    const content = field("Skill 内容", "content", "textarea", skill.content, 800);
    content.querySelector("textarea").placeholder = "写下具体的分析步骤或行动方法";
    const scope = node("div", "", "skill-scope");
    [["role", "适用身份", roleOptions, skill.role], ["action", "适用环节", actionOptions, skill.action]].forEach(([key, labelText, options, selectedValue]) => {
      const label = node("label", "", "prompt-field");
      const select = document.createElement("select");
      select.dataset.field = key;
      options.forEach(([value, name]) => { const option = new Option(name, value); select.append(option); });
      select.value = selectedValue || "";
      label.append(node("span", labelText), select);
      scope.append(label);
    });
    const enabled = node("label", "", "skill-enabled");
    const check = document.createElement("input");
    check.type = "checkbox";
    check.dataset.field = "enabled";
    check.checked = skill.enabled !== false;
    enabled.append(check, node("span", "本 Skill 已启用"));
    card.append(heading, name, summary, when, scope, content, enabled);
    return card;
  }

  async function loadPrompts() {
    const data = await api("/api/werewolf/prompts");
    renderPromptFields(data.prompts || {});
    $("saveBadge").textContent = data.saved ? "已保存" : "尚未保存";
    $("promptsCard").classList.remove("hidden");
  }

  function renderRoster(students) {
    const previousRoster = rosterSignature ? JSON.parse(rosterSignature) : [];
    rosterSignature = JSON.stringify(students);
    rosterCount = students.length;
    const validIDs = new Set(students.map((student) => student.id));
    selected = new Set([...selected].filter((id) => validIDs.has(id)));
    if (tournamentSelected === null) tournamentSelected = new Set(validIDs);
    else {
      const previous = new Set(previousRoster.map((student) => student.id));
      tournamentSelected = new Set([...tournamentSelected].filter((id) => validIDs.has(id)));
      students.forEach((student) => { if (!previous.has(student.id)) tournamentSelected.add(student.id); });
    }
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
          node("small", student.prompts_saved ? "已保存狼人杀设计" : "尚未保存狼人杀设计"));
      label.append(input, copy);
      label.classList.toggle("selected", input.checked);
      root.append(label);
    });
    const tournamentRoster = $("tournamentRoster");
    tournamentRoster.replaceChildren();
    students.forEach((student) => {
      const label = node("label", "");
      const input = document.createElement("input");
      input.type = "checkbox";
      input.value = student.id;
      input.checked = tournamentSelected.has(student.id);
      input.disabled = tournamentRunning || tournamentActionBusy;
      input.onchange = () => {
        if (input.checked) tournamentSelected.add(student.id);
        else tournamentSelected.delete(student.id);
        updateTournamentSelection();
      };
      const copy = node("span", "");
      copy.append(node("strong", `${student.id} · ${student.name}`),
        node("small", student.prompts_saved ? "已写 Agent 设计" : "尚未写 Agent 设计"));
      label.append(input, copy);
      tournamentRoster.append(label);
    });
    const designSelect = $("studentDesignSelect");
    const previousDesign = designSelect.value;
    designSelect.replaceChildren();
    students.forEach((student) => designSelect.append(new Option(`${student.id} · ${student.name}`, student.id)));
    if (validIDs.has(previousDesign)) designSelect.value = previousDesign;
    if (designSelect.value) loadStudentDesign(designSelect.value);
    updateSelection();
    updateTournamentSelection();
  }

  function updateTournamentSelection() {
    const count = tournamentSelected?.size || 0;
    $("tournamentSelectedCount").textContent = `已选 ${count} / ${rosterCount} 人`;
    $("tournamentRoster").querySelectorAll("label").forEach((label) => {
      label.classList.toggle("selected", label.querySelector("input").checked);
      label.querySelector("input").disabled = tournamentRunning || tournamentActionBusy;
    });
    $("startTournament").disabled = count < 6 || tournamentRunning || tournamentActionBusy || !$("teacherDisabled").classList.contains("hidden");
    $("selectAllTournament").disabled = tournamentRunning || tournamentActionBusy;
    $("selectNoneTournament").disabled = tournamentRunning || tournamentActionBusy;
  }

  async function loadStudentDesign(id) {
    const root = $("studentDesignView");
    root.replaceChildren(node("p", "正在读取设计…"));
    try {
      const data = await api(`/api/teacher/werewolf/students/${encodeURIComponent(id)}/design`);
      if ($("studentDesignSelect").value !== id) return;
      root.replaceChildren();
      if (!data.saved) {
        root.append(node("p", "这位学生尚未保存狼人杀 Soul 或 Skill。"));
        return;
      }
      const soul = node("section", "", "student-design-soul");
      soul.append(node("h4", "Soul"), node("p", data.prompts.general || "（未填写）"));
      root.append(soul);
      const skills = data.prompts.skills || [];
      root.append(node("h4", `Skill · ${skills.length} 个`));
      if (!skills.length) root.append(node("p", "尚未创建 Skill。"));
      skills.forEach((skill) => {
        const card = node("section", "", "student-design-skill");
        const scope = [roleOptions.find(([value]) => value === (skill.role || ""))?.[1] || "所有身份",
          actionOptions.find(([value]) => value === (skill.action || ""))?.[1] || "所有环节"].join(" · ");
        card.append(node("h5", `${skill.name} · ${skill.enabled ? "已启用" : "未启用"}`),
          node("small", scope), ...(skill.summary ? [node("p", `能力：${skill.summary}`)] : []),
          node("p", `何时使用：${skill.when_to_use}`), node("pre", skill.content));
        root.append(card);
      });
    } catch (error) {
      root.replaceChildren(node("p", `读取失败：${error.message}`));
    }
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
    if (teacher && !tournament && currentTournamentID) {
      matchListKey = "";
      renderMatchList([]);
      replayPlayback?.reset();
      $("replayCard").classList.add("hidden");
    }
    if (teacher && tournament?.id && tournament.id !== currentTournamentID) {
      tournamentSelected = new Set((data.standings || []).map((entry) => entry.student_id));
      $("tournamentRoster").querySelectorAll("input").forEach((input) => { input.checked = tournamentSelected.has(input.value); });
    }
    if (teacher && !tournament && currentTournamentID) {
      tournamentSelected = new Set([...$("tournamentRoster").querySelectorAll("input")].map((input) => input.value));
      $("tournamentRoster").querySelectorAll("input").forEach((input) => { input.checked = true; });
    }
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
      $("startTournament").disabled = !enabled || tournamentRunning || tournamentActionBusy || (tournamentSelected?.size || 0) < 6;
      $("allowSystem").disabled = tournamentRunning || tournamentActionBusy;
      $("pauseTournament").classList.toggle("hidden", !tournamentRunning);
      $("resumeTournament").classList.toggle("hidden", !tournament || !["stopped", "failed", "interrupted"].includes(tournament.status));
      $("resumeTournament").disabled = tournamentActionBusy || !enabled;
      $("clearTournament").classList.toggle("hidden", !tournament);
      $("clearTournament").disabled = tournamentRunning || tournamentActionBusy;
      $("stopGame").classList.toggle("hidden", tournamentRunning || $("startGame").dataset.running !== "true");
      updateSelection();
      updateTournamentSelection();
    }
  }

  function renderGame(game) {
    if (teacher) teacherGame = game;
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
          ? "本局记录已生成，可逐步展示到结局。"
          : `本局已${game.status === "failed" ? "出错" : "停止"}。`;
      }
      updateSelection();
      updateTeacherStage();
    }
  }

  function updateTeacherStage() {
    if (!teacherGame || !livePlayback) return;
    const step = livePlayback.snapshot();
    const revealed = step.event?.type === "result";
    if (revealed) {
      $("gameBadge").textContent = "已结束";
      $("gameMeta").textContent = `第 ${teacherGame.round} 轮 · 结局 · ${teacherGame.winner === "wolves" ? "狼人获胜" : teacherGame.winner === "villagers" ? "好人获胜" : "平局"}`;
    } else {
      $("gameBadge").textContent = "逐步展示";
      $("gameMeta").textContent = step.event
        ? `已展示 ${step.index} / ${step.total} 步 · ${phases[step.event.phase] || step.event.phase}`
        : "点击“下一步”开始展示交流流程。";
    }
    $("players").querySelectorAll(".player").forEach((card, index) => {
      const player = teacherGame.players?.[index];
      if (!player) return;
      card.classList.toggle("dead", revealed && !player.alive);
      card.querySelector("small").textContent = revealed ? (player.alive ? "存活" : "出局") : "身份已分配";
    });
  }

  function updateReplayStage() {
    if (!replayGame || !replayPlayback) return;
    const step = replayPlayback.snapshot();
    const revealed = step.event?.type === "result";
    const winner = { wolves: "狼人获胜", villagers: "好人获胜", draw: "平局" }[replayGame.winner] || "已结束";
    $("replayTitle").textContent = `第 ${replayMatchIndex + 1} 场${revealed ? ` · ${winner}` : " · 逐步回放"}`;
    $("replayMeta").textContent = step.event
      ? `已展示 ${step.index} / ${step.total} 步 · ${phases[step.event.phase] || step.event.phase}`
      : `共 ${step.total} 步交流，点击“下一步”开始。`;
    const players = $("replayPlayers");
    players.replaceChildren();
    (replayGame.players || []).forEach((player) => {
      const card = node("div", "", `player${revealed && !player.alive ? " dead" : ""}`);
      card.append(node("strong", `${player.seat} 号 · ${player.name}`),
        node("small", revealed ? (player.alive ? "存活" : "出局") : "身份已分配"),
        node("span", roles[player.role] || player.role, `role ${player.role}`));
      players.append(card);
    });
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
        [["狼人杀 Soul", event.werewolf_soul], ...(event.action_skills || []).map((skill) =>
          [`可用 Skill · ${skill.name}`, `${skill.summary ? `${skill.summary}\n` : ""}${skill.when_to_use}\n${skill.content}`])].forEach(([label, value]) => {
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

  function createPlayback(prefix, timelineID, statusID, live = false) {
    const state = { events: [], index: 0, playing: false, timer: null };
    const paint = () => {
      const timeline = $(timelineID);
      timeline.replaceChildren();
      if (state.index) appendEvents(state.events.slice(0, state.index), timelineID, false);
      else timeline.append(node("div", state.events.length ? "点击“下一步”查看第一条消息。" : "等待对局消息…", "timeline-empty"));
      $(statusID).textContent = `${state.index} / ${state.events.length} 步`;
      $(`${prefix}PlayPause`).textContent = state.playing ? "❚❚ 暂停" : "▶ 播放";
      $(`${prefix}Prev`).disabled = state.index === 0;
      $(`${prefix}Restart`).disabled = state.index === 0;
      $(`${prefix}Next`).disabled = state.index >= state.events.length;
      if (prefix === "demo") $("demoProgress").textContent = `${state.index} / ${state.events.length} 步`;
      if (prefix === "demo") updateDemoStage();
      if (prefix === "live") updateTeacherStage();
      if (prefix === "replay") updateReplayStage();
      if (state.index) timeline.lastElementChild?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    };
    const pause = () => { state.playing = false; clearInterval(state.timer); state.timer = null; paint(); };
    const next = () => {
      if (state.index < state.events.length) { state.index++; paint(); }
      else if (!live) pause();
    };
    $(`${prefix}Restart`).onclick = () => { pause(); state.index = 0; paint(); };
    $(`${prefix}Prev`).onclick = () => { pause(); state.index = Math.max(0, state.index - 1); paint(); };
    $(`${prefix}Next`).onclick = () => { pause(); next(); };
    $(`${prefix}PlayPause`).onclick = () => {
      if (state.playing) { pause(); return; }
      if (!live && state.index >= state.events.length) state.index = 0;
      state.playing = true;
      paint();
      state.timer = setInterval(next, 1500);
    };
    paint();
    return {
      reset(events = []) { pause(); state.events = events; state.index = 0; paint(); },
      add(events) {
        if (!events?.length) return;
        state.events.push(...events);
        if (state.playing || state.index === 0) paint();
        else {
          $(statusID).textContent = `${state.index} / ${state.events.length} 步`;
          $(`${prefix}Next`).disabled = false;
          if (prefix === "demo") $("demoProgress").textContent = `${state.index} / ${state.events.length} 步`;
          if (prefix === "demo") updateDemoStage();
          if (prefix === "live") updateTeacherStage();
        }
      },
      close() { pause(); },
      snapshot() { return { index: state.index, total: state.events.length, event: state.events[state.index - 1] || null }; },
    };
  }

  function renderControl(enabled) {
    classroomEnabled = enabled;
    if (!manager || teacher) return;
    $("managerCard").classList.remove("hidden");
    $("controlBadge").textContent = enabled ? "学生端已开放" : "学生端已关闭";
    $("toggleWerewolf").textContent = enabled ? "关闭学生端狼人杀" : "向学生开放狼人杀";
  }

  function renderDemo(game) {
    demoGame = game;
    $("demoCard").classList.remove("hidden");
    $("demoStage").classList.toggle("hidden", !game);
    $("demoBadge").textContent = game ? ({ running: "进行中", complete: "已结束", stopped: "已停止", failed: "出错", interrupted: "已中断" }[game.status] || game.status) : "待演示";
    $("startDemo").disabled = demoBusy || game?.status === "running";
    $("stopDemo").classList.toggle("hidden", game?.status !== "running");
    if (!game) return;
    updateDemoStage();
  }

  function updateDemoStage() {
    if (!demoGame || !demoPlayback) return;
    const step = demoPlayback.snapshot();
    const revealed = step.event?.type === "result";
    $("demoPhase").textContent = step.event ? `当前展示 · ${phases[step.event.phase] || step.event.phase}` : "等待第一步";
    $("demoMeta").textContent = revealed
      ? ({ wolves: "狼人阵营获胜", villagers: "好人阵营获胜", draw: "本局平局" }[demoGame.winner] || demoGame.winner)
      : step.event ? `当前第 ${step.index} 步；共已生成 ${step.total} 步` : "点击“下一步”开始展示 Agent 交流";
    const players = $("demoPlayers");
    players.replaceChildren();
    (demoGame.players || []).forEach((player) => {
      const card = node("div", "", `demo-player ${player.mine ? "mine" : ""} ${revealed && !player.alive ? "dead" : ""}`);
      card.append(node("small", `${player.seat} 号 · ${player.mine ? "我的 Agent" : player.name}`), node("strong", roles[player.role] || player.role),
        node("span", revealed ? (player.alive ? "存活" : "出局") : "身份已分配"));
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
      demoPlayback?.reset();
    }
    if (demoGameID) {
      const result = await api(`/api/werewolf/demo/events?after=${demoCursor}`);
      demoPlayback?.add(result.events || []);
      demoCursor = result.next || demoCursor;
      if (game?.status === "complete") $("demoStatus").textContent = "对局记录已生成，可从头逐步播放；也可修改 Soul / Skill 后再次演示。";
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
        if (teacher) livePlayback?.reset();
        else $("timeline").replaceChildren();
      }
      if (gameID) {
        const result = await api(`${prefix}/games/${encodeURIComponent(gameID)}/events?after=${cursor}`);
        if (teacher) livePlayback?.add(result.events || []);
        else appendEvents(result.events || []);
        cursor = result.next || cursor;
      }
      const tournament = await api(`${prefix}/tournament`);
      renderTournament(tournament);
      if (teacher && currentTournamentID) {
        const key = `${currentTournamentID}:${tournament.tournament.completed_matches}:${tournament.tournament.status}`;
        if (key !== matchListKey) {
          const records = await api(`/api/teacher/werewolf/tournament/${encodeURIComponent(currentTournamentID)}/matches`);
          renderMatchList(records.matches || []);
          matchListKey = key;
        }
      }
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
        livePlayback = createPlayback("live", "timeline", "livePlaybackStatus", true);
        replayPlayback = createPlayback("replay", "replayTimeline", "replayPlaybackStatus");
        if (manager) {
          $("teacherBack").href = "/werewolf";
          $("teacherBack").textContent = "← 返回我的狼人杀工作台";
          $("enableLink").href = "/werewolf";
          $("enableLink").textContent = "我的狼人杀工作台";
        }
        $("teacherControls").classList.remove("hidden");
        $("studentDesignCard").classList.remove("hidden");
        const roster = await api("/api/teacher/werewolf/roster");
        renderRoster(roster.students || []);
      } else if (manager) {
        demoPlayback = createPlayback("demo", "demoTimeline", "demoPlaybackStatus", true);
        $("managerCard").classList.remove("hidden");
        $("demoCard").classList.remove("hidden");
      }
      await refresh();
      setInterval(refresh, 2500);
    } catch (error) {
      $("status").textContent = `请先返回${teacher ? "教师管理台或 A01/A02 工作台" : "学生工作台"}登录：${error.message}`;
    }
  }

  function renderMatchList(matches) {
    const root = $("matchList");
    root.replaceChildren();
    const completed = matches.filter((item) => item.status === "complete");
    if (!completed.length) { root.append(node("p", "尚无已完成的对局。")); return; }
    completed.forEach((item) => {
      const button = node("button", "", "match-item");
      button.type = "button";
      const winner = { wolves: "狼人胜", villagers: "好人胜", draw: "平局" }[item.winner] || "已完成";
      button.append(node("strong", `第 ${item.index + 1} 场 · ${winner}`),
        node("small", (item.players || []).map((player) => player.name).join("、")));
      button.onclick = () => openReplay(item.index);
      root.append(button);
    });
  }

  async function openReplay(index) {
    try {
      const data = await api(`/api/teacher/werewolf/tournament/${encodeURIComponent(currentTournamentID)}/matches/${index}`);
      replayGame = data.game;
      replayMatchIndex = index;
      replayPlayback.reset(data.events || []);
      $("replayCard").classList.remove("hidden");
      $("replayCard").scrollIntoView({ behavior: "smooth", block: "start" });
    } catch (error) { $("tournamentActionStatus").textContent = `读取回放失败：${error.message}`; }
  }

  async function savePromptDesign() {
    const payload = { general: $("werewolfSoul").value, skills: [] };
    $("werewolfSkillList").querySelectorAll(".skill-card").forEach((card) => {
      const value = (field) => card.querySelector(`[data-field=${field}]`).value;
      payload.skills.push({ name: value("name"), summary: value("summary"), when_to_use: value("when_to_use"), content: value("content"),
        role: value("role"), action: value("action"), enabled: card.querySelector("[data-field=enabled]").checked });
    });
    const result = await api("/api/werewolf/prompts", { method: "PUT", body: JSON.stringify(payload) });
    $("saveBadge").textContent = "已保存";
    $("saveStatus").textContent = "Soul 与自建 Skill 已保存；下一局将使用最新版本。";
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
      if (!$("promptForm").reportValidity()) return;
      demoBusy = true;
      $("startDemo").disabled = true;
      $("demoStatus").textContent = "正在保存 Soul 与 Skill，并为六个 Agent 分配身份…";
      try {
        await savePromptDesign();
        await api("/api/werewolf/demo", { method: "POST", body: JSON.stringify({ role: $("demoRole").value }) });
        $("demoStatus").textContent = "演示已开始。点击“下一步”逐条讲解，或点击“播放”自动展示。";
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
    $("closeReplay").onclick = () => { replayPlayback?.close(); $("replayCard").classList.add("hidden"); };
    $("studentDesignSelect").onchange = (event) => loadStudentDesign(event.target.value);
    $("selectAllTournament").onclick = () => {
      tournamentSelected = new Set([...$("tournamentRoster").querySelectorAll("input")].map((input) => input.value));
      $("tournamentRoster").querySelectorAll("input").forEach((input) => { input.checked = true; });
      updateTournamentSelection();
    };
    $("selectNoneTournament").onclick = () => {
      tournamentSelected.clear();
      $("tournamentRoster").querySelectorAll("input").forEach((input) => { input.checked = false; });
      updateTournamentSelection();
    };
    $("startTournament").onclick = async () => {
      const ids = [...tournamentSelected];
      if (ids.length < 6 || tournamentRunning || tournamentActionBusy) return;
      const matches = Math.ceil(ids.length * 20 / 6);
      const fillers = matches * 6 - ids.length * 20;
      const systemAllowed = $("allowSystem").checked;
      if (fillers && !systemAllowed) {
        $("tournamentActionStatus").textContent = `当前人数需要 ${fillers} 个系统补位席位，请勾选补位后开赛。`;
        return;
      }
      if (!confirm(`将为选中的 ${ids.length} 名学生安排每人 20 局，共 ${matches} 场六人局${fillers ? `，其中 ${fillers} 个席位由系统 Agent 补齐` : ""}。每局会多次调用 DeepSeek，可能持续较长时间并消耗 API 额度。确认开赛？`)) return;
      tournamentActionBusy = true;
      $("startTournament").disabled = true;
      $("tournamentActionStatus").textContent = "正在排赛并保存 Agent 设计…";
      try {
        await api("/api/teacher/werewolf/tournament", { method: "POST", body: JSON.stringify({ allow_system: systemAllowed, student_ids: ids }) });
        $("tournamentActionStatus").textContent = "比赛已开始，积分榜会自动更新。";
        await refresh();
      } catch (error) {
        $("tournamentActionStatus").textContent = `开赛失败：${error.message}`;
      } finally {
        tournamentActionBusy = false;
        updateTournamentSelection();
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
    $("clearTournament").onclick = async () => {
      if (!currentTournamentID || tournamentRunning || tournamentActionBusy) return;
      if (!confirm("清空当前课堂的全部比赛记录？积分榜、所有比赛对局和回放都会删除，无法恢复。学生的狼人杀 Soul 与 Skill 会保留。")) return;
      tournamentActionBusy = true;
      $("clearTournament").disabled = true;
      try {
        const cleared = await api("/api/teacher/werewolf/tournament", { method: "DELETE" });
        tournamentActionBusy = false;
        renderTournament(cleared);
        $("tournamentActionStatus").textContent = "比赛积分与对局记录已清空，可以重新开赛。";
        await refresh();
      } catch (error) {
        $("tournamentActionStatus").textContent = `清空失败：${error.message}`;
      } finally {
        tournamentActionBusy = false;
        $("clearTournament").disabled = tournamentRunning;
      }
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
