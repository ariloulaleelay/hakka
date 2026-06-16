if vim.g.loaded_hakka == 1 then
  return
end
vim.g.loaded_hakka = 1

vim.api.nvim_create_user_command("HakkaChat", function()
  require("hakka").toggle()
end, {})

vim.api.nvim_create_user_command("HakkaSend", function(opts)
  require("hakka").send_oneshot(opts.args)
end, { nargs = "+" })

vim.api.nvim_create_user_command("HakkaReset", function()
  require("hakka").reset()
end, {})
