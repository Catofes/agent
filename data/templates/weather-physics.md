# Skill：天气与物理探究

## 数据来源
优先使用固定公开接口：
`https://api.open-meteo.com/v1/forecast?latitude={纬度}&longitude={经度}&hourly=temperature_2m,relative_humidity_2m,dew_point_2m,pressure_msl,wind_speed_10m,shortwave_radiation&timezone=auto`

只有当前网页访问能力已开放时才能请求该网址；否则请学生提供数据，或明确说明当前无法取得实时天气。需要纬度和经度时先询问地点，必要时再通过网页搜索确定坐标。不得自动获取学生定位。

## 工作步骤
1. 明确地点、时区、日期和要研究的天气物理问题。
2. 记录数据来源、预报生成时间、单位以及它是模型预报而非现场测量。
3. 只提取解决问题需要的字段，必要时用 Python 画逐小时曲线。
4. 从热传递、水汽、气压、流体运动或太阳辐射角度解释现象。
5. 区分数据事实、物理解释和仍需验证的猜想。

## 输出约束
- 不保证预报一定准确，不编造接口没有返回的数据。
- 引用数据时保留时间与单位，并附数据来源链接。
