package ipguard

import "strings"

// traditionalPairs maps Traditional Chinese characters common in place names
// to Simplified ones. ipwho.is answers lang=zh-CN with Traditional names for
// some cities (美茵河畔法蘭克福); the panel shows Simplified.
const traditionalPairs = "" +
	"蘭兰爾尔頓顿倫伦羅罗馬马達达維维亞亚薩萨華华蘇苏聖圣聯联東东紐纽約约澤泽灣湾縣县區区鎮镇鄉乡莊庄島岛嶼屿灘滩濱滨漢汉陽阳臺台門门關关廣广慶庆遼辽黃黄寧宁嶺岭貴贵雲云陝陕歐欧國国際际諾诺圖图書书館馆魯鲁開开衛卫傑杰舊旧磯矶費费邁迈鳳凤鹽盐溫温婁娄奧奥靈灵橫横內内買买賽赛徹彻愛爱納纳陸陆" +
	"機机場场這这個个們们來来時时電电號号業业學学長长發发經经會会對对現现點点運运動动線线車车鐵铁橋桥寶宝興兴為为與与豐丰雙双龍龙鳥鸟魚鱼麗丽齊齐齋斋樂乐樓楼廟庙廳厅濟济滬沪潤润澗涧溝沟瀋沈遠远連连進进邊边鄭郑鄧邓陳陈陰阴隊队階阶隴陇雞鸡離离難难韓韩頂顶須须頭头題题顏颜風风飛飞馮冯騰腾驛驿體体鬆松鴻鸿鶴鹤麥麦黨党" +
	"塊块塢坞墻墙壩坝壽寿嬰婴孫孙實实將将尋寻導导屬属層层峽峡嶽岳巖岩帶带幫帮廠厂張张彎弯徑径從从復复戲戏揚扬撫抚敵敌數数斷断晉晋曉晓曆历條条棗枣楊杨樁桩檢检權权歲岁歷历殘残氣气漁渔滿满漲涨潛潜濕湿燈灯營营爐炉牆墙獅狮環环瑪玛畢毕當当礦矿碼码確确禮礼禪禅種种穀谷積积窯窑竇窦筆笔節节範范築筑簡简籃篮糧粮紅红紹绍統统絲丝綠绿網网緬缅總总織织繩绳義义習习聲声肅肃脈脉腦脑臨临舉举艦舰萬万葉叶蔣蒋蕭萧藍蓝蘆芦處处蠻蛮補补裏里裝装製制複复覺觉觀观" +
	"計计訊讯許许設设詩诗話话誠诚調调談谈論论諸诸謝谢證证識识譚谭議议讓让豬猪貓猫貝贝負负財财貨货貿贸資资賀贺賈贾賓宾賴赖贊赞趙赵躍跃軍军軒轩載载輕轻輪轮轉转辦办農农迴回週周遜逊遷迁選选遺遗還还鄒邹鄰邻醫医釋释錢钱錦锦鍾钟鏡镜閩闽閣阁闊阔隨随險险雜杂雖虽靜静韋韦順顺預预領领額额顯显飯饭養养餘余駐驻驗验鮑鲍鳴鸣鷹鹰" +
	"灣湾鄂鄂廈厦漳漳臺台蓮莲濰潍贛赣滇滇黔黔閔闵鎰镒鈴铃鋼钢鐘钟銀银銅铜錫锡鐮镰鑫鑫萊莱塢坞堯尧舜舜衝冲濤涛潟潟滷卤瑋玮璽玺甌瓯畝亩盧卢碩硕礎础穩稳竊窃絡络綿绵縱纵纜缆羨羡膠胶葦苇蔭荫薊蓟蘊蕴蟹蟹衚胡襄襄訂订詹詹誌志謙谦譽誉讀读貞贞賢贤贏赢軟软輔辅轄辖遞递鄱鄱銘铭鋪铺錄录鏈链闆板陣阵隆隆雁雁頌颂頻频餐餐騎骑驅驱鬱郁鮮鲜鵬鹏麒麒黎黎鼎鼎齡龄"

var simplifier = func() *strings.Replacer {
	pairs := []rune(traditionalPairs)
	args := make([]string, 0, len(pairs))
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i] != pairs[i+1] {
			args = append(args, string(pairs[i]), string(pairs[i+1]))
		}
	}
	return strings.NewReplacer(args...)
}()

// Simplified turns the Traditional characters it knows into Simplified ones.
func Simplified(s string) string { return simplifier.Replace(s) }
