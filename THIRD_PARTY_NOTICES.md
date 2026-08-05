# 第三方软件声明

本项目自身的源码采用仓库根目录 [`LICENSE`](LICENSE) 中的 MIT License。
第三方组件仍分别受其原始许可证约束；本项目的 MIT License 不会替代、扩展或
缩减这些第三方许可证。

## 当前发布范围

当前公开仓库仅发布本项目源码，不发布预编译 EXE，也不包含 Go module cache、
`vendor`、`node_modules` 或前端 `dist` 构建产物。第三方依赖的准确版本由
`go.mod`、`go.sum` 和 `frontend/package-lock.json` 锁定，安装时由 Go 或 npm
从其上游来源取得。

暂不发布预编译二进制的直接原因是：本项目依赖的
`github.com/bogdanfinn/fhttp v0.6.8` 在本次核验到的 Go 模块包中没有独立的
`LICENSE`、`LICENCE`、`COPYING` 或 `NOTICE` 文件。该包的许多源文件声明其受
“BSD-style”许可证约束，并指向一个 `LICENSE` 文件，但该文件没有随此版本的
模块包提供。在确认完整、可再分发的许可证文本之前，本项目不分发包含该依赖的
预编译二进制。此处仅记录核验结果，不替上游指定许可证。

## 随本仓库源码一同提供的 Wails 材料

仓库中的 Wails 生成运行时、构建模板及默认图标材料来源于：

- 项目：`github.com/wailsapp/wails/v2`
- 核验版本：`v2.12.0`
- 许可证：MIT License
- 涉及位置包括 `frontend/wailsjs/runtime/`、`build/appicon.png` 及相关 Wails
  构建材料。

原始许可证文本：

```text
MIT License

Copyright (c) 2018-Present Lea Anthony

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## bogdanfinn/tls-client

- 项目：`github.com/bogdanfinn/tls-client`
- 核验版本：`v1.15.1`
- 许可证：上游 `LICENSE` 所载四条款 BSD 文本

以下为该版本 `LICENSE` 的完整原文：

```text
Copyright (c) 2023, Bogdan Finn
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:
1. Redistributions of source code must retain the above copyright
   notice, this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright
   notice, this list of conditions and the following disclaimer in the
   documentation and/or other materials provided with the distribution.
3. All advertising materials mentioning features or use of this software
   must display the following acknowledgement:
   This product includes software developed by the <organization>.
4. Neither the name of the <organization> nor the
   names of its contributors may be used to endorse or promote products
   derived from this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDER ''AS IS'' AND ANY
EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE
USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

注意：上游许可证原文自身使用了字面占位符 `<organization>`；为忠实保留原文，
本声明没有擅自替换该占位符。

## bogdanfinn/fhttp 的核验记录

- 项目：`github.com/bogdanfinn/fhttp`
- 核验版本：`v0.6.8`
- 模块声明：`module github.com/bogdanfinn/fhttp`
- 核验结果：模块根目录未见独立许可证或 NOTICE 文件。

该版本包括大量带有下列头部声明的 Go 源文件：

```text
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
```

不同文件的版权年份可能不同，但许可证引用方式相同。由于被引用的 `LICENSE`
没有随 `v0.6.8` 模块包提供，本项目不把上述声明推断或改写成某个具体 SPDX
许可证，也不会在该问题解决前发布链接了该组件的预编译二进制。

## 后续二进制发布要求

如果以后发布 EXE 或其他二进制，发布前至少需要：

1. 取得或确认 `fhttp v0.6.8` 完整且可再分发的许可证文本，或者替换该依赖；
2. 按最终实际链接和打包结果重新生成完整的第三方组件清单；
3. 将所有要求随二进制提供的版权、许可证和 NOTICE 文本一同交付；
4. 复核 `tls-client` 许可证中的二进制分发及宣传材料条款。
