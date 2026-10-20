package config

// The tracking page's defaults, written from what Nigerian and international
// couriers tell receivers (inspect on delivery, the signature as proof, a
// short window to report damage, liability set by the sender's contract).
// A starting point for the operator to make their own, with their lawyer:
// every word is editable on the Settings page.

const DefaultTerms = `These terms cover receiving a delivery from {company}. The sender's agreement with {company} also applies.

Checking your goods
Please check the goods when they arrive, before you sign. If anything is damaged, missing or opened, tell the driver and note it when you sign.

Signing for your delivery
Your name and signature on the driver's phone confirm you received the goods in the condition you saw. We may hand the delivery to anyone at the address who appears to be allowed to receive it, such as reception, security or a family member. They sign the same way.

Damage or missing items
Report it to {company} on {support_phone} within 24 hours of delivery, with your delivery reference and photos. Damage inside packaging that was intact at delivery may not be covered.

If nobody is available
The driver will try to call. If the delivery can't be handed over, it is marked "customer unavailable" and {company} will arrange another attempt. A further attempt may be charged to the sender.

Times
Arrival times on this page are estimates from the truck's position and traffic. They can change.

Liability
{company}'s responsibility for loss or damage is limited to what the sender's agreement sets out, usually the declared value of the goods. It does not cover indirect losses.

Your information
Your name, phone number, signature and any delivery photo are used to deliver your goods and to settle questions about the delivery. They are shown on this tracking page, which only people with the link can open. Please don't share the link publicly.`

// DefaultFAQ is a function so callers can't share and change one slice.
func DefaultFAQ() []FAQItem {
	return []FAQItem{
		{"When will my delivery arrive?", "Once the truck is on its way, this page shows an estimated arrival time. It's worked out from where the truck is and typical traffic, so it can change. The page updates on its own."},
		{"Why isn't the truck moving on the map?", "The map shows the truck's last reported position. If the driver's phone loses signal, the page says how old the position is. The delivery is still on its way."},
		{"Can someone else receive it for me?", "Yes. Anyone at the address who can receive it, such as reception, security or a family member, can sign for it. Let them know it's coming."},
		{"What happens if I'm not available?", "The driver will call you. If nobody can receive it, the delivery is marked as not delivered and {company} will contact you to arrange another attempt."},
		{"Do I have to sign?", "Yes. The receiver signs on the driver's phone. It's the proof that the goods arrived. Check the goods before you sign."},
		{"Can I change the address or the time?", "Contact {company} on {support_phone} as early as you can. The driver can't change the delivery on the way."},
		{"Something is damaged or missing. What do I do?", "Tell the driver before signing if you can. Then report it to {company} on {support_phone} within 24 hours, with your delivery reference and photos."},
		{"Is this link private?", "Only people with the link can open this page. Don't post it publicly."},
	}
}
