class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.2.5"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.5/reminal_1.2.5_darwin_arm64.tar.gz"
      sha256 "1447175d008886ef2eea1c7e23122eb3b5e993572c76be637a138566958c15c4"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.5/reminal_1.2.5_darwin_amd64.tar.gz"
      sha256 "4fa68cbc1362f8ca637fa2f13ea808bc7c891e3112594293e4a09eb30c86369a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.5/reminal_1.2.5_linux_arm64.tar.gz"
      sha256 "ed6a31e6267e7915adb55489371d3b2381288889a5b5205788839095b31e91a2"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.5/reminal_1.2.5_linux_amd64.tar.gz"
      sha256 "8bba3f2c9dd156aa1b9db3558bd32a374e7b8ca37da7e68e746c5a3499fa7e02"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
