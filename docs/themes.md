# Ghostty 与主题

[返回 README](../README.md)

我们强烈建议使用 [Ghostty](https://ghostty.org/) 作为 Fleetty 的终端客户端。本地运行与 SSH 远程连接均可使用；Ghostty 的系统深浅色切换可直接驱动 Fleetty 的自动主题，无需向服务器转发主题环境变量。

仓库提供配套的 [Fleetty Dark](../themes/ghostty/Fleetty%20Dark) 和 [Fleetty Light](../themes/ghostty/Fleetty%20Light) 主题：深色使用炭黑背景与柔和亮色，浅色使用暖纸背景与深色强调色，浅色光标为纯黑。

在客户端电脑上安装主题（不需要在服务器上安装）：

```bash
fleetty_theme_dir="${XDG_CONFIG_HOME:-$HOME/.config}/ghostty/themes"
mkdir -p "$fleetty_theme_dir"
curl -fL "https://github.com/Rhythmicc/Fleetty/releases/latest/download/Fleetty-Dark.ghostty" \
  -o "$fleetty_theme_dir/Fleetty Dark"
curl -fL "https://github.com/Rhythmicc/Fleetty/releases/latest/download/Fleetty-Light.ghostty" \
  -o "$fleetty_theme_dir/Fleetty Light"
```

将 Ghostty 配置中的 `theme` 设置替换为：

```ini
theme = dark:Fleetty Dark,light:Fleetty Light
```

重新加载 Ghostty 配置后，终端会[跟随系统外观切换主题](https://ghostty.org/docs/features/theme#separate-light-and-dark-themes)。终端主题控制 shell 和终端色板，Fleetty 自己控制监控界面的语义配色。

为了保持稳定的对比度，建议在 Ghostty 配置中使用 `background-opacity = 1`；透明背景会受窗口后方内容影响。主题文件只提供颜色，不修改字体、快捷键或透明度设置。其他终端仍可使用 Fleetty；自动跟随取决于终端是否支持相应的深浅色报告，不支持时可按 `t` 手动切换。

## Fleetty 自动主题

`fleetty top` 默认使用 `--theme auto`，启动时读取终端背景与深浅色报告，并订阅运行中的深浅色变化；直接 SSH 连接 Fleetty 的监控端口同样按客户端终端选择主题，无需转发环境变量。`--theme dark` / `--theme light` 或 `DEFAULT_THEME=dark|light` 可固定配色；按 `t` 后当前会话改为手动选择，不再跟随系统。不响应查询的终端使用深色，仍可按 `t` 切换。

## Powerlevel10k

使用蓝色底的 Python／Conda 环境段时，应搭配浅色文字；不要沿用暗色文字。以下配置放入 `~/.p10k.zsh`，适用于 Fleetty 的深浅色主题：

```zsh
typeset -g POWERLEVEL9K_OS_ICON_FOREGROUND=7
typeset -g POWERLEVEL9K_OS_ICON_BACKGROUND=0
typeset -g POWERLEVEL9K_DIR_FOREGROUND=254
typeset -g POWERLEVEL9K_VIRTUALENV_FOREGROUND=254
typeset -g POWERLEVEL9K_ANACONDA_FOREGROUND=254
typeset -g POWERLEVEL9K_PYENV_FOREGROUND=254
```

深色主题的普通蓝色用于深蓝底，亮蓝色用于文字；不要将两者设成同一个浅蓝色。修改 Ghostty 主题后重新加载终端配置（macOS 默认 `⌘⇧,`）；修改 prompt 后执行 `source ~/.p10k.zsh`。
