<?php
// Pure entity doubles. No kernel, Doctrine, connection or database is loaded.
declare(strict_types=1);
namespace MauticPlugin\MauticMetaBundle\Entity {
    class MetaAsset { public function __construct(private string $id) {} public function getExternalId(): string { return $this->id; } }
    class MetaMessage {
        public string $type = 'unsupported'; public array $payload; public ?\DateTimeImmutable $modified = null;
        public function __construct(public MetaAsset $asset, public string $recipient = '5511999999999', public string $direction = 'outbound') { $this->payload = ['message'=>['id'=>'original','text'=>'','unsupported'=>true,'timestamp'=>1600000000],'whatsqr'=>['sent_from_device'=>true,'historical'=>false]]; }
        public function getMessageType(): string { return $this->type; } public function setMessageType(string $v): self {$this->type=$v;return $this;}
        public function getAsset(): MetaAsset {return $this->asset;} public function getRecipient(): string {return $this->recipient;} public function getDirection(): string {return $this->direction;}
        public function getPayload(): array {return $this->payload;} public function setPayload(array $p): self {$this->payload=$p;return $this;} public function setDateModified(\DateTimeImmutable $d): self {$this->modified=$d;return $this;}
    }
}
namespace {
require __DIR__.'/../../Domain/AttachmentReference.php';require __DIR__.'/../../Domain/MessageContent.php';require __DIR__.'/../../Domain/MessageContentRecovery.php';
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;use MauticPlugin\MauticWhatsQrBundle\Domain\MessageContentRecovery;use MauticPlugin\MauticWhatsQrBundle\Domain\MessageContent;
function check(bool $ok): void {if (!$ok) throw new \RuntimeException('Content recovery regression');}
$a=new MetaAsset('account-a');$good=['id'=>'original','unsupported'=>false,'text'=>'Contato','content_type'=>'contact'];
$m=new MetaMessage($a);check(MessageContentRecovery::apply($m,$a,$good,$m->recipient,true));check($m->type==='contact' && $m->payload['message']['timestamp']===1600000000 && $m->payload['whatsqr']['historical']===false && $m->modified!==null);
check(!MessageContentRecovery::apply($m,$a,$good,$m->recipient,true));
foreach (['account','peer','direction','id','view-once','blank','unsupported','attachment-large','attachment-empty','attachment-error','not-qr','known','old-view-once'] as $case) {
 $m=new MetaMessage($a);$incoming=$good;$asset=$a;$peer=$m->recipient;$fromMe=true;
 switch($case) {
  case 'account':$asset=new MetaAsset('account-b');break;
  case 'peer':$peer='5511999990000';break;
  case 'direction':$fromMe=false;break;
  case 'id':$incoming['id']='different';break;
  case 'view-once':$incoming['content_type']='view_once';break;
  case 'blank':$incoming['text']=' ';break;
  case 'unsupported':$incoming['unsupported']=true;break;
  case 'attachment-large':$incoming['attachment']=['type'=>'image','id'=>str_repeat('a',64),'file_size'=>33554433];break;
  case 'attachment-empty':$incoming['attachment']=['type'=>'image','id'=>str_repeat('a',64),'file_size'=>0];break;
  case 'attachment-error':$incoming['attachment']=['type'=>'image','id'=>str_repeat('a',64),'file_size'=>128,'error'=>'unavailable'];break;
  case 'not-qr':unset($m->payload['whatsqr']);break;
  case 'known':$m->type='text';break;
  case 'old-view-once':$m->payload['whatsqr']['unsupported_reason']='view_once';break;
 }
 $before=$m->payload;check(!MessageContentRecovery::apply($m,$asset,$incoming,$peer,$fromMe));check($m->payload===$before && $m->modified===null);
}
$m=new MetaMessage($a);$incoming=$good;$incoming['attachment']=['type'=>'image','id'=>str_repeat('a',64),'file_size'=>128,'filename'=>'../photo.png','media_key'=>'SECRET'];
check(MessageContentRecovery::apply($m,$a,$incoming,$m->recipient,true));check($m->type==='image' && $m->payload['message']['image']['filename']==='photo.png');check(!str_contains(json_encode($m->payload),'SECRET'));
check(MessageContent::type(['content_type'=>'evil'])==='text');check(MessageContent::reason(['unsupported_reason'=>'SECRET'])===null);
echo "Content recovery and security cases passed; no database or kernel.\n";
}
