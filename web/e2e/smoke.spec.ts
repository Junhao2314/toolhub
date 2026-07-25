import { expect, test } from '@playwright/test'

test('login and core navigation render without overlap', async ({ page }, testInfo) => {
  const email = process.env.TOOLHUB_E2E_EMAIL
  const username = process.env.TOOLHUB_E2E_USERNAME ?? 'admin'
  const password = process.env.TOOLHUB_E2E_PASSWORD
  if (!email || !password) throw new Error('TOOLHUB_E2E_EMAIL and TOOLHUB_E2E_PASSWORD are required')
  const consoleErrors: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'ToolHub' })).toBeVisible()
  await page.getByLabel('Username or email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(page.getByText('Tailnet control plane')).toBeVisible()
  if (process.env.TOOLHUB_E2E_EXPECT_PASSWORD_CHANGE === 'true') {
    await expect(page.getByText('This account is using a temporary password.')).toBeVisible()
  }

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Account', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()
  await expect(page.getByLabel('Username')).toHaveValue(username)
  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Overview', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()

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

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Nodes', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Nodes' })).toBeVisible()
  await expect(page.getByText('Project host', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Configure pinned SSH fallback' }).first().click()
  await expect(page.getByRole('heading', { name: /SSH fallback/ })).toBeVisible()
  await expect(page.getByLabel('Pinned known_hosts line')).toBeVisible()
  await expect(page.getByLabel('Private key')).toBeVisible()
  await page.getByRole('button', { name: 'Close', exact: true }).click()
  await page.getByRole('button', { name: 'Enroll project host' }).click()
  await expect(page.getByRole('heading', { name: /Enroll project host/ })).toBeVisible()
  await expect(page.getByLabel('Node name')).toHaveValue('project-host')
  await page.getByRole('button', { name: 'Close', exact: true }).click()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-nodes.png`, fullPage: true })
  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.getByLabel('Username or email').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  expect(consoleErrors).toEqual([])
})
