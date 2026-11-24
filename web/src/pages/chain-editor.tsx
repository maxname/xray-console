import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  api,
  type Chain,
  type ChainRequest,
  type ChainStrategy,
  type Server,
} from '@/lib/api'

import { STRATEGIES, explain } from '@/lib/chains'

export interface HopDraft {
  servers: number[]
  strategy: ChainStrategy
  reserve: number
}

export function ChainEditor({
  chain,
  servers,
  onClose,
  onSaved,
}: {
  chain: Chain | null
  servers: Server[]
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(chain?.name ?? '')
  const [description, setDescription] = useState(chain?.description ?? '')
  const [hops, setHops] = useState<HopDraft[]>(
    chain
      ? chain.hops.map((hop) => ({
          servers: hop.servers.map((server) => server.id),
          strategy: hop.strategy,
          reserve: hop.reserve?.id ?? 0,
        }))
      : [{ servers: [], strategy: 'leastPing', reserve: 0 }],
  )

  const save = useMutation({
    mutationFn: () => {
      const body: ChainRequest = { name: name.trim(), description: description.trim(), hops }
      return chain ? api.updateChain(chain.id, body) : api.createChain(body)
    },
    onSuccess: onSaved,
  })

  const setHop = (index: number, change: Partial<HopDraft>) =>
    setHops((current) =>
      current.map((hop, at) => (at === index ? { ...hop, ...change } : hop)),
    )
  // A server may sit in one place of a chain only: everything taken elsewhere.
  const takenElsewhere = (index: number) =>
    new Set(
      hops.flatMap((hop, at) =>
        at === index ? [] : [...hop.servers, ...(hop.reserve ? [hop.reserve] : [])],
      ),
    )

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    save.mutate()
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl">
        <form className="grid gap-4" onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>
              {chain
                ? t('chains.editor.titleEdit', { name: chain.name })
                : t('chains.editor.titleNew')}
            </DialogTitle>
            <DialogDescription>{t('chains.editor.description')}</DialogDescription>
          </DialogHeader>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field>
              <Label htmlFor="chain-name">{t('chains.editor.name')}</Label>
              <Input
                id="chain-name"
                placeholder={t('chains.editor.namePlaceholder')}
                autoComplete="off"
                required
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <Field>
              <Label htmlFor="chain-description">{t('chains.editor.about')}</Label>
              <Input
                id="chain-description"
                autoComplete="off"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
              />
            </Field>
          </div>

          <div className="flex flex-col gap-3">
            <span className="font-medium">{t('chains.editor.hops')}</span>
            {hops.map((hop, index) => (
              <HopEditor
                key={index}
                index={index}
                hop={hop}
                servers={servers}
                taken={takenElsewhere(index)}
                removable={hops.length > 1 && index > 0}
                onChange={(change) => setHop(index, change)}
                onRemove={() =>
                  setHops((current) => current.filter((_, at) => at !== index))
                }
              />
            ))}
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="self-start"
              onClick={() =>
                setHops((current) => [
                  ...current,
                  { servers: [], strategy: 'leastPing', reserve: 0 },
                ])
              }
            >
              {t('chains.editor.addHop')}
            </Button>
          </div>

          {save.isError && <Notice title={explain(t, save.error)} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? t('chains.editor.saving') : t('chains.editor.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function HopEditor({
  index,
  hop,
  servers,
  taken,
  removable,
  onChange,
  onRemove,
}: {
  index: number
  hop: HopDraft
  servers: Server[]
  taken: Set<number>
  removable: boolean
  onChange: (change: Partial<HopDraft>) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const entry = index === 0
  const available = servers.filter((server) => !taken.has(server.id))
  const members = available.filter((server) => server.id !== hop.reserve)
  const reserves = available.filter((server) => !hop.servers.includes(server.id))

  const toggle = (id: number) => {
    if (entry) {
      // The entry is one server: choosing another replaces it.
      onChange({ servers: [id] })
      return
    }
    onChange({
      servers: hop.servers.includes(id)
        ? hop.servers.filter((member) => member !== id)
        : [...hop.servers, id],
    })
  }

  return (
    <fieldset className="flex flex-col gap-3 rounded-lg border p-3">
      <div className="flex items-center justify-between gap-2">
        <legend className="float-left font-medium">
          {entry
            ? t('chains.editor.hopEntry')
            : t('chains.editor.hopNumber', { number: index + 1 })}
        </legend>
        {removable && (
          <Button type="button" variant="ghost" size="sm" onClick={onRemove}>
            {t('chains.editor.removeHop')}
          </Button>
        )}
      </div>
      {entry && (
        <span className="text-xs text-muted-foreground">{t('chains.editor.entryHint')}</span>
      )}

      {members.length === 0 ? (
        <span className="text-muted-foreground">{t('chains.editor.noServersLeft')}</span>
      ) : (
        <div className="flex flex-wrap gap-x-4 gap-y-1.5">
          {members.map((server) => (
            <label key={server.id} className="inline-flex cursor-pointer items-center gap-2">
              <input
                type={entry ? 'radio' : 'checkbox'}
                name={`chain-hop-${index}`}
                className="size-4 accent-primary"
                checked={hop.servers.includes(server.id)}
                onChange={() => toggle(server.id)}
              />
              <span>
                {server.name}
                {server.location !== '' && (
                  <span className="text-xs text-muted-foreground"> · {server.location}</span>
                )}
              </span>
            </label>
          ))}
        </div>
      )}

      {!entry && hop.servers.length > 1 && (
        <div className="flex flex-col gap-1.5" role="radiogroup" aria-label={t('chains.editor.strategy')}>
          <span className="font-medium">{t('chains.editor.strategy')}</span>
          {STRATEGIES.map((strategy) => (
            <label key={strategy} className="flex cursor-pointer items-start gap-2">
              <input
                type="radio"
                name={`chain-strategy-${index}`}
                className="mt-1 size-4 accent-primary"
                checked={hop.strategy === strategy}
                onChange={() => onChange({ strategy })}
              />
              <span className="flex flex-col">
                <span>{t(`chains.editor.strategies.${strategy}`)}</span>
                <span className="text-xs text-muted-foreground">
                  {t(`chains.editor.strategies.${strategy}Hint`)}
                </span>
              </span>
            </label>
          ))}
        </div>
      )}

      {!entry && (
        <Field hint={t('chains.editor.reserveHint')}>
          <Label htmlFor={`chain-reserve-${index}`}>{t('chains.editor.reserve')}</Label>
          <Select
            value={String(hop.reserve)}
            onValueChange={(value) => onChange({ reserve: Number(value) })}
          >
            <SelectTrigger id={`chain-reserve-${index}`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="0">{t('chains.editor.reserveNone')}</SelectItem>
              {reserves.map((server) => (
                <SelectItem key={server.id} value={String(server.id)}>
                  {server.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
    </fieldset>
  )
}

export function DeleteChainDialog({
  chain,
  open,
  onOpenChange,
  onDeleted,
}: {
  chain: Chain
  open: boolean
  onOpenChange: (open: boolean) => void
  onDeleted: () => void
}) {
  const { t } = useTranslation()
  const remove = useMutation({
    mutationFn: () => api.deleteChain(chain.id),
    onSuccess: () => {
      onOpenChange(false)
      onDeleted()
    },
  })
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('chains.deleteDialog.title', { name: chain.name })}</DialogTitle>
          <DialogDescription>{t('chains.deleteDialog.description')}</DialogDescription>
        </DialogHeader>
        {remove.isError && <Notice title={explain(t, remove.error)} />}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
            {t('chains.deleteDialog.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
