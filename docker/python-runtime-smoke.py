import warnings
from pathlib import Path

import matplotlib
import matplotlib.pyplot as plt
import networkx
import numpy
import openpyxl
import pandas
import scipy
import seaborn
import sklearn
import sympy
import xlsxwriter
from PIL import Image
from matplotlib import font_manager


output = Path("/tmp/python-runtime-chinese.png")
warnings.filterwarnings("error", message=r"Glyph .* missing from font")
font_path = font_manager.findfont("Noto Sans CJK JP", fallback_to_default=False)
if not Path(font_path).is_file():
    raise RuntimeError("Noto Sans CJK JP is unavailable")

x = numpy.linspace(-numpy.pi, numpy.pi, 200)
fig, axis = plt.subplots()
axis.plot(x, numpy.sin(x), label="正弦函数")
axis.set_title("中文函数图像")
axis.set_xlabel("自变量")
axis.set_ylabel("函数值")
axis.legend()
fig.savefig(output)
plt.close(fig)

with Image.open(output) as image:
    image.verify()

print(
    "python runtime ready:",
    matplotlib.__version__,
    networkx.__version__,
    numpy.__version__,
    openpyxl.__version__,
    pandas.__version__,
    scipy.__version__,
    seaborn.__version__,
    sklearn.__version__,
    sympy.__version__,
    xlsxwriter.__version__,
    font_path,
)
