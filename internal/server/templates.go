package server

type presetSkillDefinition struct {
	ID        string
	File      string
	Category  string
	Name      string
	Summary   string
	WhenToUse string
}

type presetSkillOptionDTO struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Name     string `json:"name"`
	Summary  string `json:"summary"`
}

var presetSkillCatalog = []presetSkillDefinition{
	{ID: "python-beginner", File: "python-beginner.md", Category: "basic", Name: "Python 入门教练", Summary: "用预测、最小代码、运行和解释帮助学生逐步学习 Python。", WhenToUse: "学生学习 Python 基础语法、读代码或完成入门练习时。"},
	{ID: "python-debugger", File: "python-debugger.md", Category: "basic", Name: "Python 调试教练", Summary: "定位最早出现的错误，引导学生用小步实验自行修复。", WhenToUse: "学生提供报错、异常输出或不能正常工作的 Python 程序时。"},
	{ID: "socratic-coach", File: "socratic-coach.md", Category: "basic", Name: "苏格拉底式学习教练", Summary: "通过逐层追问帮助学生自己形成解题路径。", WhenToUse: "学生希望学习、理解或解决问题，而不是只索取最终答案时。"},
	{ID: "quiz-master", File: "quiz-master.md", Category: "basic", Name: "出题与练习教练", Summary: "根据知识点生成分层练习，并在作答后逐步反馈。", WhenToUse: "学生希望练习、测验或检查某个学科知识点时。"},

	{ID: "physics-problem", File: "physics-problem.md", Category: "physics", Name: "物理解题教练", Summary: "从研究对象、相互作用和物理模型出发组织解题。", WhenToUse: "学生处理力学、电学、热学、光学等物理问题时。"},
	{ID: "physics-experiment", File: "physics-experiment.md", Category: "physics", Name: "物理实验设计师", Summary: "设计变量、测量步骤、数据记录与误差控制方案。", WhenToUse: "学生需要设计、改进或评价一个物理实验时。"},
	{ID: "physics-data", File: "physics-data.md", Category: "physics", Name: "物理实验数据分析", Summary: "分析少量测量数据、拟合关系并解释误差与物理意义。", WhenToUse: "学生提供测量数据、CSV、Excel 或实验图表时。"},
	{ID: "physics-modeling", File: "physics-modeling.md", Category: "physics", Name: "物理建模与仿真", Summary: "明确假设、建立方程，再用 Python 数值模拟并核验结果。", WhenToUse: "学生需要模拟运动、振动、电路或其他随时间变化的系统时。"},
	{ID: "physics-formula-check", File: "physics-formula-check.md", Category: "physics", Name: "公式、量纲与数量级核验", Summary: "检查推导、量纲、单位、边界条件和数量级。", WhenToUse: "学生希望验证物理公式、推导过程或计算结果时。"},
	{ID: "weather-physics", File: "weather-physics.md", Category: "physics", Name: "天气与物理探究", Summary: "从固定天气网页或搜索结果提取数据，形成可验证的物理问题。", WhenToUse: "学生希望查询天气并研究温度、湿度、气压、风或辐射规律时。"},

	{ID: "math-problem", File: "math-problem.md", Category: "math", Name: "数学解题教练", Summary: "识别条件、目标与关键关系，分层提示而非直接代做。", WhenToUse: "学生解决代数、函数、数列、概率等数学问题时。"},
	{ID: "proof-coach", File: "proof-coach.md", Category: "math", Name: "数学证明教练", Summary: "帮助明确命题、条件、论证结构与逻辑缺口。", WhenToUse: "学生需要理解、补全、检查或撰写数学证明时。"},
	{ID: "function-graph", File: "function-graph.md", Category: "math", Name: "函数与图像探究", Summary: "连接解析式、参数、图像特征和变化规律。", WhenToUse: "学生研究函数图像、参数变化、零点、极值或交点时。"},
	{ID: "geometry-explorer", File: "geometry-explorer.md", Category: "math", Name: "几何探究教练", Summary: "通过作图、猜想、证明和反例组织几何探索。", WhenToUse: "学生解决平面几何、解析几何或空间几何问题时。"},
	{ID: "probability-statistics", File: "probability-statistics.md", Category: "math", Name: "概率与统计实验", Summary: "用模拟、表格和图像建立随机性与统计规律的联系。", WhenToUse: "学生学习概率、抽样、分布、期望或统计推断时。"},
}

func presetSkillOptions() []presetSkillOptionDTO {
	out := make([]presetSkillOptionDTO, 0, len(presetSkillCatalog))
	for _, item := range presetSkillCatalog {
		out = append(out, presetSkillOptionDTO{ID: item.ID, Category: item.Category, Name: item.Name, Summary: item.Summary})
	}
	return out
}

func presetSkillByID(id string) (presetSkillDefinition, bool) {
	for _, item := range presetSkillCatalog {
		if item.ID == id {
			return item, true
		}
	}
	return presetSkillDefinition{}, false
}
