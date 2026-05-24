/**
 * Card — paper-surface container.
 *
 * Variants:
 *   - flat   No shadow; border only (default — Linear-quiet)
 *   - raised Elev-1 shadow (use sparingly, e.g. floating panel)
 *
 * Composition:
 *   <Card>
 *     <Card.Header>…</Card.Header>
 *     <Card.Body>…</Card.Body>
 *     <Card.Footer>…</Card.Footer>
 *   </Card>
 */

function Card({ variant = 'flat', className = '', children, ...rest }) {
  const cn = [
    'bg-paper text-ink border border-line rounded-lg overflow-hidden',
    variant === 'raised' ? 'shadow-e1' : '',
    className,
  ]
    .filter(Boolean)
    .join(' ')
  return (
    <div className={cn} {...rest}>
      {children}
    </div>
  )
}

Card.Header = function CardHeader({ className = '', children, ...rest }) {
  return (
    <div
      className={`px-4 py-3 border-b border-line flex items-center gap-3 ${className}`}
      {...rest}
    >
      {children}
    </div>
  )
}

Card.Body = function CardBody({ className = '', children, ...rest }) {
  return (
    <div className={`p-4 ${className}`} {...rest}>
      {children}
    </div>
  )
}

Card.Footer = function CardFooter({ className = '', children, ...rest }) {
  return (
    <div
      className={`px-4 py-3 border-t border-line bg-bg-elev2 flex items-center gap-2 ${className}`}
      {...rest}
    >
      {children}
    </div>
  )
}

export default Card
