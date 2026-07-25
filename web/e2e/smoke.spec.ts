import { expect, test } from '@playwright/test'

test('login and core navigation render without overlap', async ({ page }, testInfo) => {
  const email = process.env.TOOLHUB_E2E_EMAIL
  const password = process.env.TOOLHUB_E2E_PASSWORD
  if (!email || !password) throw new Error('TOOLHUB_E2E_EMAIL and TOOLHUB_E2E_PASSWORD are required')
  const consoleErrors: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'ToolHub' })).toBeVisible()
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(page.getByText('Tailnet control plane')).toBeVisible()

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Skills', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Skills' })).toBeVisible()
  await expect(page.locator('main')).toHaveCSS('overflow-x', 'visible')

  const viewport = page.viewportSize()!
  const header = await page.locator('.page-header').boundingBox()
  const toolbar = await page.locator('.toolbar').boundingBox()
  expect(header).not.toBeNull()
  expect(toolbar).not.toBeNull()
  expect(header!.y + header!.height).toBeLessThanOrEqual(toolbar!.y + 1)
  expect(header!.x).toBeGreaterThanOrEqual(0)
  expect(header!.x + header!.width).toBeLessThanOrEqual(viewport.width + 1)

  await page.screenshot({ path: `../test-results/${testInfo.project.name}-skills.png`, fullPage: true })
  expect(consoleErrors).toEqual([])
})
